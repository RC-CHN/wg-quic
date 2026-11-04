package armorbind

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/conn"
)

const (
	fecExpiryPoll             = 500 * time.Millisecond
	maxSessionTelemetry       = 256
	maxRecentSessionTelemetry = 64
	recentSessionTelemetryTTL = 5 * time.Minute
	maxSessionEvents          = 4096
	maxSessionEventQuery      = 1024
)

type receivedPacket struct {
	data []byte
	ep   *receiveEndpoint
	// release returns data to its pool after the receive worker copies it
	// out. Nil for frames owned by the FEC decoder.
	release     func([]byte)
	reservation *session
}

type outboundPacket struct {
	preparedFrame []byte
	id            uint64
	queuedAt      time.Time // data only; priority messages have reserved admission
}

type runState struct {
	ctx             context.Context
	cancel          context.CancelFunc
	carrier         *quiccarrier.Carrier
	recv            chan receivedPacket
	cfg             Config
	mu              sync.Mutex
	sessions        map[uint64]*session
	endpoints       map[netip.AddrPort]*Endpoint
	closing         bool
	inboundSessions int
	pendingInbound  int

	reassembly *reassembler
	wg         sync.WaitGroup
}

func (s *runState) startWorker(worker func()) bool {
	s.mu.Lock()
	if s.closing || s.ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		worker()
	}()
	return true
}

type Bind struct {
	cfg                    Config
	mu                     sync.Mutex
	state                  *runState
	sessionRestored        func(netip.AddrPort)
	nextPacket             atomic.Uint64
	nextSession            atomic.Uint64
	receiveSequence        atomic.Uint64
	mark                   atomic.Uint32
	stats                  bindStats
	obfsResolved           map[netip.AddrPort]obfs.Key
	obfsDynamic            map[netip.AddrPort]endpointKeyLease
	obfsReceive            map[obfs.Key]receiveKeyLease
	fecPolicies            map[netip.AddrPort]endpointFECPolicyLease
	telemetryMu            sync.Mutex
	recentSessions         []telemetry.ClosedSessionObservation
	recentSequence         uint64
	recentEvicted          uint64
	replacements           map[uint64]uint64
	events                 sessionEventLog
	receiveOverflowMu      sync.Mutex
	receiveOverflowCarrier *quiccarrier.Carrier
	receiveOverflowBase    uint64
	receiveOverflowRawLast uint64
	receiveOverflowTotal   uint64
}

// ReceiveSequence returns the last carrier-ingress sequence assigned to a
// WireGuard packet. Core snapshots it after installing an endpoint generation
// so already-queued authenticated packets cannot satisfy new readiness.
func (b *Bind) ReceiveSequence() uint64 {
	return b.receiveSequence.Load()
}

// ObservationID identifies this counter lifetime without exposing configuration.
func (b *Bind) ObservationID() string { return b.events.streamID }

type endpointKeyLease struct {
	key  obfs.Key
	refs int
}

type receiveKeyLease struct {
	refs    int
	release func()
}

type endpointFECPolicyLease struct {
	policy string
	refs   int
}

var _ conn.Bind = (*Bind)(nil)

func (b *Bind) debugf(format string, args ...any) {
	if b.cfg.Debugf != nil {
		b.cfg.Debugf(format, args...)
	}
}

func (b *Bind) eventf(format string, args ...any) {
	if b.cfg.Eventf != nil {
		b.cfg.Eventf(format, args...)
	}
}

// SetSessionRestored installs the upper-layer hook run after an automatic
// QUIC reconnect succeeds. It must be configured before Open.
func (b *Bind) SetSessionRestored(callback func(netip.AddrPort)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionRestored = callback
}

func (b *Bind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	ctx, cancel := context.WithCancel(context.Background())
	carrier, err := quiccarrier.Open(port, quiccarrier.Config{
		HandshakeTimeout: b.cfg.HandshakeTimeout,
		MaxIdleTimeout:   b.cfg.MaxIdleTimeout,
		KeepAlivePeriod:  b.cfg.KeepAlivePeriod,
		CongestionMode:   b.cfg.CongestionMode,
		Mark:             b.mark.Load(),
		ObfsMode:         b.cfg.ObfsMode,
		ObfsKeys:         b.cfg.ObfsKeys,
		EndpointKeys:     b.obfsResolved,

		DatagramReceiveQueueCapacity: b.cfg.DatagramReceiveQueueCapacity,
	})
	if err != nil {
		cancel()
		return nil, 0, err
	}
	for key, lease := range b.obfsReceive {
		lease.release = carrier.AcquireReceiveKey(key)
		b.obfsReceive[key] = lease
	}
	state := &runState{
		ctx: ctx, cancel: cancel, carrier: carrier,
		recv: make(chan receivedPacket, b.cfg.QueueSize), cfg: b.cfg,
		sessions: make(map[uint64]*session), endpoints: make(map[netip.AddrPort]*Endpoint), reassembly: newReassembler(),
	}
	b.state = state
	state.wg.Add(1)
	go b.acceptLoop(state)
	b.debugf(
		"ArmorBind opened: udp_port=%d fec=%s fec_data_shards=%d obfs=%s queue_size=%d",
		carrier.Port(), b.cfg.FECMode, b.cfg.FECDataShards, b.cfg.ObfsMode, b.cfg.QueueSize,
	)
	return []conn.ReceiveFunc{b.receiveFunc(state)}, carrier.Port(), nil
}

func (b *Bind) Close() error {
	b.mu.Lock()
	state := b.state
	b.state = nil
	for key, lease := range b.obfsReceive {
		lease.release = nil
		b.obfsReceive[key] = lease
	}
	b.mu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	state.closing = true
	for _, sess := range state.sessions {
		sess.setCloseCause(telemetry.SessionCloseLocalShutdown, "local_shutdown", nil)
		sess.cancel()
	}
	state.mu.Unlock()
	state.cancel()
	carrierErr := state.carrier.Close()
	state.wg.Wait()
	stats := b.Stats()
	b.debugf(
		"ArmorBind closed: wg_tx=%d wg_rx=%d wire_tx=%d wire_rx=%d queue_drops=%d fec_recovered=%d fec_unrecovered=%d",
		stats.WGTxPackets, stats.WGRxPackets, stats.WireTxPackets, stats.WireRxPackets,
		stats.QueueDrops, stats.FECRecovered, stats.FECUnrecovered,
	)
	return carrierErr
}

// AcquireReceiveKey makes one Salamander key available to the inbound decoder
// before a peer is committed. The returned release is reference-counted and
// remains valid across bind close/reopen cycles.
func (b *Bind) AcquireReceiveKey(key obfs.Key) func() {
	b.mu.Lock()
	lease := b.obfsReceive[key]
	lease.refs++
	if lease.refs == 1 && b.state != nil {
		lease.release = b.state.carrier.AcquireReceiveKey(key)
	}
	b.obfsReceive[key] = lease
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { b.releaseReceiveKey(key) })
	}
}

func (b *Bind) releaseReceiveKey(key obfs.Key) {
	b.mu.Lock()
	lease, exists := b.obfsReceive[key]
	if !exists {
		b.mu.Unlock()
		return
	}
	lease.refs--
	if lease.refs > 0 {
		b.obfsReceive[key] = lease
		b.mu.Unlock()
		return
	}
	delete(b.obfsReceive, key)
	release := lease.release
	b.mu.Unlock()
	if release != nil {
		release()
	}
}

func (b *Bind) SetMark(mark uint32) error {
	b.mark.Store(mark)
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return nil
	}
	return state.carrier.SetMark(mark)
}
func (b *Bind) BatchSize() int { return 32 }

func (b *Bind) Port() uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == nil {
		return 0
	}
	return b.state.carrier.Port()
}

func (b *Bind) ParseEndpoint(value string) (conn.Endpoint, error) {
	addrPort, err := peerendpoint.ParseNumeric(value)
	if err != nil {
		return nil, fmt.Errorf("endpoint must be a numeric IP address: %w", err)
	}
	ep := &Endpoint{
		owner: b,
		addr:  addrPort,
	}
	ep.route.Store(&endpointRoute{configured: true})
	b.mu.Lock()
	_, associated := b.obfsResolved[ep.addr]
	policy := b.fecPolicies[ep.addr].policy
	state := b.state
	b.mu.Unlock()
	ep.fecPolicy = policy
	if state != nil {
		state.mu.Lock()
		if existing := state.endpoints[ep.addr]; existing != nil {
			ep = existing
			ep.mu.Lock()
			ep.retired = false
			ep.fecPolicy = policy
			ep.route.Store(&endpointRoute{configured: true})
			ep.mu.Unlock()
		} else {
			state.endpoints[ep.addr] = ep
		}
		state.mu.Unlock()
	}
	b.debugf("resolved peer endpoint: configured=%q resolved=%s obfs_key_associated=%t", value, ep.addr, associated)
	return ep, nil
}

func (b *Bind) Send(bufs [][]byte, endpoint conn.Endpoint) error {
	var owner *Bind
	switch ep := endpoint.(type) {
	case *Endpoint:
		if ep != nil {
			owner = ep.owner
		}
	case *receiveEndpoint:
		if ep != nil {
			owner = ep.owner
		}
	}
	if owner != b {
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return net.ErrClosed
	}
	var sess *session
	var err error
	switch ep := endpoint.(type) {
	case *Endpoint:
		sess, err = b.sessionForEndpoint(state, ep, false)
	case *receiveEndpoint:
		sess, err = b.sessionForReceivedEndpoint(state, ep)
	}
	if err != nil {
		return err
	}
	var admission sendAdmission
	defer admission.stop()
	for _, buf := range bufs {
		if len(buf) == 0 || len(buf) > maxDatagramSize {
			return fmt.Errorf("invalid WireGuard datagram size %d", len(buf))
		}
		b.stats.wgTxPackets.Add(1)
		b.stats.wgTxBytes.Add(uint64(len(buf)))
		sess.stats.wgTxPackets.Add(1)
		sess.stats.wgTxBytes.Add(uint64(len(buf)))
		queue := sess.send
		copies := 1
		if controlWireGuardDatagram(buf) {
			queue = sess.priority
		}
		if priorityWireGuardDatagram(buf) {
			// Duplicate handshake/keepalive datagrams so a burst cannot wipe
			// out both copies at once; WireGuard dedupes the repeat by its
			// counter, so the duplicated send is protocol-safe.
			copies = 2
		}
		admitted := true
		for i := 0; i < copies && admitted; i++ {
			preparedFrame := quiccarrier.AcquireDatagramSendBuffer(frameHeaderSize + len(buf))
			copy(preparedFrame[frameHeaderSize:], buf)
			packet := outboundPacket{
				preparedFrame: preparedFrame,
				id:            b.nextPacket.Add(1),
			}
			if queue == sess.send {
				packet.queuedAt = time.Now()
				reason, err := sess.enqueueData(packet, &admission)
				if err != nil || reason != "" {
					quiccarrier.ReleaseDatagramSendBuffer(preparedFrame)
					if err != nil {
						if state.ctx.Err() != nil {
							return net.ErrClosed
						}
						// A retiring session must wake blocked senders, but
						// the bind remains usable while its peer reconnects.
						reason = "send_session_closed"
					}
					// Queue rejection is silent: per-packet WireGuard error
					// logging would itself amplify overload.
					sess.recordSendDrop(reason)
					admitted = false
				}
				continue
			}
			select {
			case queue <- packet:
			case <-sess.ctx.Done():
				sess.releaseSendBytes(packet)
				quiccarrier.ReleaseDatagramSendBuffer(preparedFrame)
				if state.ctx.Err() != nil {
					return net.ErrClosed
				}
				sess.recordSendDrop("send_session_closed")
				admitted = false
			default:
				sess.releaseSendBytes(packet)
				quiccarrier.ReleaseDatagramSendBuffer(preparedFrame)
				b.stats.queueDrops.Add(1)
				sess.stats.queueDrops.Add(1)
				if sess.claimQueueDropEvent(time.Now()) {
					observation := sess.telemetry(time.Now())
					b.recordSessionEventAt(
						sess.id, sess.generation, telemetry.SessionEventQueueDrop,
						"send_queue_full", observation.SampledAt, nil,
						sessionEventMetricsFromStats(
							observation.Stats, sess.currentFECPolicy(), observation.CurrentEndpoint,
						),
					)
					b.debugf(
						"send queue full: session=%d endpoint=%s drops=%d",
						sess.id, sess.endpoint.addr, sess.stats.queueDrops.Load(),
					)
				}
				// Drop silently instead of returning an error: WireGuard
				// logs one error per rejected packet, so a per-packet error
				// amplifies overload. The authoritative per-packet signal is
				// the queueDrops counter.
				admitted = false
			}
		}
	}
	return nil
}

func (b *Bind) sessionForReceivedEndpoint(state *runState, ep *receiveEndpoint) (*session, error) {
	state.mu.Lock()
	if state.closing || state.ctx.Err() != nil {
		state.mu.Unlock()
		return nil, net.ErrClosed
	}
	sess := ep.session
	if sess != nil && sess.state == state && !sess.closed.Load() {
		state.mu.Unlock()
		return sess, nil
	}
	state.mu.Unlock()
	if fallback := ep.currentRoute().fallback; fallback != nil {
		return b.sessionForEndpoint(state, fallback, false)
	}
	return nil, errors.New("cannot dial a connection-scoped peer endpoint")
}

func (b *Bind) receiveFunc(state *runState) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		if len(packets) == 0 || len(sizes) < len(packets) || len(eps) < len(packets) {
			return 0, errors.New("invalid receive buffers")
		}
		var first receivedPacket
		select {
		case first = <-state.recv:
		case <-state.ctx.Done():
			return 0, net.ErrClosed
		}
		n := 0
		put := func(packet receivedPacket) error {
			defer packet.dispose()
			if len(packets[n]) < len(packet.data) {
				return fmt.Errorf("receive buffer is %d bytes, need %d", len(packets[n]), len(packet.data))
			}
			sizes[n] = copy(packets[n], packet.data)
			eps[n] = packet.ep
			n++
			return nil
		}
		if err := put(first); err != nil {
			return 0, err
		}
		for n < len(packets) {
			select {
			case packet := <-state.recv:
				if err := put(packet); err != nil {
					return n, err
				}
			default:
				return n, nil
			}
		}
		return n, nil
	}
}
