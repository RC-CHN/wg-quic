package armorbind

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
	"github.com/RC-CHN/wg-quic/internal/transport/fec"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/conn"
)

const fecExpiryPoll = 500 * time.Millisecond

type Config struct {
	QueueSize        int
	HandshakeTimeout time.Duration
	MaxIdleTimeout   time.Duration
	KeepAlivePeriod  time.Duration
	ReconnectMin     time.Duration
	ReconnectMax     time.Duration
	ReconnectStable  time.Duration
	ReconnectJitter  func(time.Duration) time.Duration
	CongestionMode   string
	FECMode          string
	FECDataShards    int
	FECInterleave    int
	FECFlushDeadline time.Duration
	ObfsMode         string
	ObfsKeys         []obfs.Key
	Eventf           func(format string, args ...any)
	Debugf           func(format string, args ...any)
}

func DefaultConfig() Config {
	return Config{
		QueueSize:        1024,
		HandshakeTimeout: 4 * time.Second,
		MaxIdleTimeout:   15 * time.Second,
		KeepAlivePeriod:  5 * time.Second,
		ReconnectMin:     250 * time.Millisecond,
		ReconnectMax:     30 * time.Second,
		ReconnectStable:  10 * time.Second,
		ReconnectJitter: func(value time.Duration) time.Duration {
			return value * time.Duration(900+rand.IntN(201)) / 1000
		},
		CongestionMode:   "model",
		FECMode:          "auto",
		FECDataShards:    fec.MaxDataShards,
		FECInterleave:    1,
		FECFlushDeadline: 2 * time.Millisecond,
		ObfsMode:         "none",
	}
}

type receivedPacket struct {
	data []byte
	ep   *Endpoint
	// release returns data to its pool after the receive worker copies it
	// out. Nil for frames owned by the FEC decoder.
	release func([]byte)
}

type outboundPacket struct {
	preparedFrame []byte
	id            uint64
}

type runState struct {
	ctx       context.Context
	cancel    context.CancelFunc
	carrier   *quiccarrier.Carrier
	recv      chan receivedPacket
	cfg       Config
	mu        sync.Mutex
	sessions  map[uint64]*session
	endpoints map[netip.AddrPort]*Endpoint
	closing   bool

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
	cfg             Config
	mu              sync.Mutex
	state           *runState
	sessionRestored func(netip.AddrPort)
	nextPacket      atomic.Uint64
	nextSession     atomic.Uint64
	mark            atomic.Uint32
	stats           bindStats
	obfsResolved    map[netip.AddrPort]obfs.Key
	obfsDynamic     map[netip.AddrPort]endpointKeyLease
}

type endpointKeyLease struct {
	key  obfs.Key
	refs int
}

var _ conn.Bind = (*Bind)(nil)

type bindStats struct {
	wgTxPackets    atomic.Uint64
	wgTxBytes      atomic.Uint64
	wgRxPackets    atomic.Uint64
	wgRxBytes      atomic.Uint64
	wireTxPackets  atomic.Uint64
	wireTxBytes    atomic.Uint64
	wireRxPackets  atomic.Uint64
	wireRxBytes    atomic.Uint64
	queueDrops     atomic.Uint64
	fecDataTx      atomic.Uint64
	fecParityTx    atomic.Uint64
	fecRawLost     atomic.Uint64
	fecRecovered   atomic.Uint64
	fecUnrecovered atomic.Uint64
	activeSessions atomic.Uint64
}

type EndpointSessionState string

type EndpointReconnectStatus struct {
	Attempts      uint64
	Failures      uint64
	NextReconnect int64
}

const (
	EndpointSessionIdle         EndpointSessionState = "idle"
	EndpointSessionDialing      EndpointSessionState = "dialing"
	EndpointSessionReconnecting EndpointSessionState = "reconnecting"
	EndpointSessionEstablished  EndpointSessionState = "established"
)

func New(cfg Config) *Bind {
	defaults := DefaultConfig()
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaults.QueueSize
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = defaults.HandshakeTimeout
	}
	if cfg.MaxIdleTimeout <= 0 {
		cfg.MaxIdleTimeout = defaults.MaxIdleTimeout
	}
	if cfg.KeepAlivePeriod <= 0 {
		cfg.KeepAlivePeriod = defaults.KeepAlivePeriod
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = defaults.ReconnectMin
	}
	if cfg.ReconnectMax < cfg.ReconnectMin {
		cfg.ReconnectMax = defaults.ReconnectMax
	}
	if cfg.ReconnectStable <= 0 {
		cfg.ReconnectStable = defaults.ReconnectStable
	}
	if cfg.ReconnectJitter == nil {
		cfg.ReconnectJitter = defaults.ReconnectJitter
	}
	if cfg.CongestionMode == "" || cfg.CongestionMode == "auto" {
		cfg.CongestionMode = defaults.CongestionMode
	}
	if cfg.FECMode == "" {
		cfg.FECMode = defaults.FECMode
	}
	if cfg.FECDataShards <= 0 {
		cfg.FECDataShards = defaults.FECDataShards
	}
	if cfg.FECInterleave <= 0 {
		cfg.FECInterleave = defaults.FECInterleave
	}
	if cfg.FECFlushDeadline <= 0 {
		cfg.FECFlushDeadline = defaults.FECFlushDeadline
	}
	if cfg.ObfsMode == "" {
		cfg.ObfsMode = defaults.ObfsMode
	}
	cfg.ObfsKeys = append([]obfs.Key(nil), cfg.ObfsKeys...)
	return &Bind{
		cfg: cfg, obfsResolved: make(map[netip.AddrPort]obfs.Key),
		obfsDynamic: make(map[netip.AddrPort]endpointKeyLease),
	}
}

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
	})
	if err != nil {
		cancel()
		return nil, 0, err
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
	b.mu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	state.closing = true
	for _, sess := range state.sessions {
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

func (b *Bind) Stats() telemetry.Stats {
	stats := telemetry.Stats{
		WGTxPackets: b.stats.wgTxPackets.Load(), WGTxBytes: b.stats.wgTxBytes.Load(),
		WGRxPackets: b.stats.wgRxPackets.Load(), WGRxBytes: b.stats.wgRxBytes.Load(),
		WireTxPackets: b.stats.wireTxPackets.Load(), WireTxBytes: b.stats.wireTxBytes.Load(),
		WireRxPackets: b.stats.wireRxPackets.Load(), WireRxBytes: b.stats.wireRxBytes.Load(),
		QueueDrops: b.stats.queueDrops.Load(), FECDataTx: b.stats.fecDataTx.Load(),
		FECParityTx: b.stats.fecParityTx.Load(), FECRawLost: b.stats.fecRawLost.Load(),
		FECRecovered: b.stats.fecRecovered.Load(), FECUnrecovered: b.stats.fecUnrecovered.Load(),
		ActiveSessions: b.stats.activeSessions.Load(),
	}
	b.addQUICStats(&stats)
	return stats
}

func (b *Bind) addQUICStats(stats *telemetry.Stats) {
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	sessions := make([]*session, 0, len(state.sessions))
	for _, sess := range state.sessions {
		sessions = append(sessions, sess)
	}
	state.mu.Unlock()
	for _, sess := range sessions {
		stats.SendQueueDepth += uint64(len(sess.send))
		stats.PriorityQueueDepth += uint64(len(sess.priority))
		stats.ControlQueueDepth += uint64(len(sess.control))
		if sess.fecEncoder != nil {
			parity, lossEstimatePPM := sess.fecEncoder.Stats()
			stats.FECCurrentParityShards = max(stats.FECCurrentParityShards, uint64(parity))
			stats.FECLossEstimatePPM = max(stats.FECLossEstimatePPM, lossEstimatePPM)
		}
		sess.mu.Lock()
		conn := sess.conn
		sess.mu.Unlock()
		if conn == nil {
			continue
		}
		current := conn.Stats()
		stats.QUICBytesAcked += current.BytesAcked
		stats.QUICPacketsAcked += current.PacketsAcked
		stats.QUICBytesLost += current.BytesLost
		stats.QUICPacketsLost += current.PacketsLost
		stats.QUICCongestionWindowBytes += current.CongestionWindow
		stats.QUICBytesInFlight += current.BytesInFlight
		stats.QUICBandwidthEstimateBps += current.BandwidthEstimate
		stats.QUICPacingRateBps += current.PacingRate
		stats.QUICPathRTTUs = max(
			stats.QUICPathRTTUs,
			uint64(current.PropagationRTT/time.Microsecond),
		)
		stats.QUICQueueDelayUs = max(
			stats.QUICQueueDelayUs,
			uint64(current.QueueDelay/time.Microsecond),
		)
		stats.QUICFECRecoverableLossPPM = max(
			stats.QUICFECRecoverableLossPPM,
			current.FECRecoverableLossPPM,
		)
		stats.QUICFECResidualLossPPM = max(
			stats.QUICFECResidualLossPPM,
			current.FECResidualLossPPM,
		)
		stats.QUICCongestionModelState = max(
			stats.QUICCongestionModelState,
			current.CongestionModelState,
		)
		stats.QUICDatagramSendQueueLen += current.DatagramSendQueueLen
		minRTTUs := uint64(current.MinRTT / time.Microsecond)
		if minRTTUs != 0 && (stats.QUICMinRTTUs == 0 || minRTTUs < stats.QUICMinRTTUs) {
			stats.QUICMinRTTUs = minRTTUs
		}
		stats.QUICSmoothedRTTUs = max(
			stats.QUICSmoothedRTTUs,
			uint64(current.SmoothedRTT/time.Microsecond),
		)
		stats.QUICLatestRTTUs = max(
			stats.QUICLatestRTTUs,
			uint64(current.LatestRTT/time.Microsecond),
		)
	}
}

func (b *Bind) Port() uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == nil {
		return 0
	}
	return b.state.carrier.Port()
}

func (b *Bind) EndpointSessionState(endpoint netip.AddrPort) EndpointSessionState {
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return EndpointSessionIdle
	}
	state.mu.Lock()
	configuredEndpoint := state.endpoints[endpoint]
	sessions := make([]*session, 0, 1)
	for _, candidate := range state.sessions {
		candidate.mu.Lock()
		remote := candidate.remoteAddr
		candidate.mu.Unlock()
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		if remote == endpoint {
			sessions = append(sessions, candidate)
		}
	}
	state.mu.Unlock()
	result := EndpointSessionIdle
	for _, session := range sessions {
		if session.closed.Load() {
			continue
		}
		session.mu.Lock()
		established := session.conn != nil
		session.mu.Unlock()
		if established {
			return EndpointSessionEstablished
		}
		result = EndpointSessionDialing
	}
	if result == EndpointSessionIdle && configuredEndpoint != nil {
		configuredEndpoint.mu.Lock()
		reconnecting := configuredEndpoint.reconnectScheduled
		configuredEndpoint.mu.Unlock()
		if reconnecting {
			return EndpointSessionReconnecting
		}
	}
	return result
}

func (b *Bind) EndpointReconnectStatus(endpoint netip.AddrPort) EndpointReconnectStatus {
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return EndpointReconnectStatus{}
	}
	state.mu.Lock()
	ep := state.endpoints[endpoint]
	state.mu.Unlock()
	if ep == nil {
		return EndpointReconnectStatus{}
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	result := EndpointReconnectStatus{
		Attempts: ep.reconnectAttempts,
		Failures: ep.reconnectFailures,
	}
	if !ep.nextReconnect.IsZero() {
		result.NextReconnect = ep.nextReconnect.Unix()
	}
	return result
}

func (b *Bind) ParseEndpoint(value string) (conn.Endpoint, error) {
	addrPort, err := peerendpoint.ParseNumeric(value)
	if err != nil {
		return nil, fmt.Errorf("endpoint must be a numeric IP address: %w", err)
	}
	ep := &Endpoint{
		owner:      b,
		addr:       addrPort,
		configured: true,
	}
	b.mu.Lock()
	_, associated := b.obfsResolved[ep.addr]
	state := b.state
	b.mu.Unlock()
	if state != nil {
		state.mu.Lock()
		if existing := state.endpoints[ep.addr]; existing != nil {
			ep = existing
			ep.mu.Lock()
			ep.configured = true
			ep.retired = false
			ep.mu.Unlock()
		} else {
			state.endpoints[ep.addr] = ep
		}
		state.mu.Unlock()
	}
	b.debugf("resolved peer endpoint: configured=%q resolved=%s obfs_key_associated=%t", value, ep.addr, associated)
	return ep, nil
}

// AcquireEndpointKey associates a runtime-selected numeric endpoint with its
// peer's Salamander key. The returned release function is reference-counted so
// multiple peers or endpoint generations can safely share the same tuple.
func (b *Bind) AcquireEndpointKey(endpoint netip.AddrPort, key obfs.Key) (func(), error) {
	endpoint, err := peerendpoint.Canonical(endpoint)
	if err != nil {
		return nil, fmt.Errorf("valid numeric endpoint is required: %w", err)
	}
	b.mu.Lock()
	lease, ok := b.obfsDynamic[endpoint]
	if ok && lease.key != key {
		b.mu.Unlock()
		return nil, fmt.Errorf("endpoint %s is already associated with a different obfuscation key", endpoint)
	}
	lease.key = key
	lease.refs++
	b.obfsDynamic[endpoint] = lease
	b.obfsResolved[endpoint] = key
	state := b.state
	if state != nil {
		state.carrier.AssociateEndpoint(endpoint, key)
	}
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { b.releaseEndpointKey(endpoint, key) })
	}, nil
}

func (b *Bind) releaseEndpointKey(endpoint netip.AddrPort, key obfs.Key) {
	b.mu.Lock()
	lease, ok := b.obfsDynamic[endpoint]
	if !ok || lease.key != key {
		b.mu.Unlock()
		return
	}
	lease.refs--
	if lease.refs > 0 {
		b.obfsDynamic[endpoint] = lease
		b.mu.Unlock()
		return
	}
	delete(b.obfsDynamic, endpoint)
	delete(b.obfsResolved, endpoint)
	state := b.state
	if state != nil {
		state.carrier.DisassociateEndpoint(endpoint, key)
	}
	b.mu.Unlock()
}

// RetireEndpoint closes the configured outbound session for an old endpoint.
// It does not affect accepted connection-scoped endpoints used for roaming.
func (b *Bind) RetireEndpoint(endpoint netip.AddrPort) {
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	ep := state.endpoints[endpoint]
	delete(state.endpoints, endpoint)
	state.mu.Unlock()
	if ep == nil {
		return
	}
	ep.mu.Lock()
	ep.retired = true
	ep.activated = false
	ep.cancelReconnectLocked()
	session := ep.session
	ep.session = nil
	ep.mu.Unlock()
	if session != nil {
		session.cancel()
	}
}

// RedialEndpoint closes the current configured session while retaining the
// endpoint object and immediately starts a fresh QUIC session.
func (b *Bind) RedialEndpoint(endpoint netip.AddrPort) {
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	ep := state.endpoints[endpoint]
	state.mu.Unlock()
	if ep == nil {
		return
	}
	ep.mu.Lock()
	ep.cancelReconnectLocked()
	session := ep.session
	ep.session = nil
	ep.mu.Unlock()
	if session != nil {
		session.cancel()
	}
	b.scheduleEndpointReconnect(state, ep, true)
}

func (b *Bind) scheduleEndpointReconnect(
	state *runState,
	ep *Endpoint,
	immediate bool,
) {
	ep.mu.Lock()
	if !ep.configured || !ep.activated || ep.retired ||
		ep.session != nil || state.ctx.Err() != nil {
		ep.mu.Unlock()
		return
	}
	if ep.reconnectScheduled {
		if !immediate {
			ep.mu.Unlock()
			return
		}
		ep.cancelReconnectLocked()
	}
	delay := time.Duration(0)
	if !immediate && ep.consecutiveFailures > 0 {
		delay = reconnectBackoff(
			state.cfg.ReconnectMin,
			state.cfg.ReconnectMax,
			ep.consecutiveFailures-1,
		)
		delay = state.cfg.ReconnectJitter(delay)
		if delay < 0 {
			delay = 0
		}
	}
	ctx, cancel := context.WithCancel(state.ctx)
	ep.reconnectGeneration++
	generation := ep.reconnectGeneration
	ep.reconnectScheduled = true
	ep.reconnectCancel = cancel
	if delay > 0 {
		ep.nextReconnect = time.Now().Add(delay)
	} else {
		ep.nextReconnect = time.Time{}
	}
	remote := ep.addr
	ep.mu.Unlock()

	started := state.startWorker(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		ep.mu.Lock()
		if ep.reconnectGeneration != generation || !ep.reconnectScheduled {
			ep.mu.Unlock()
			return
		}
		ep.reconnectScheduled = false
		ep.reconnectCancel = nil
		ep.nextReconnect = time.Time{}
		eligible := ep.configured && ep.activated && !ep.retired && ep.session == nil
		ep.mu.Unlock()
		if !eligible || state.ctx.Err() != nil {
			return
		}
		if b.hasEstablishedSession(state, remote) {
			return
		}
		if _, err := b.sessionForEndpoint(state, ep, true); err != nil && state.ctx.Err() == nil {
			b.debugf("start maintained QUIC session: remote=%s error=%v", remote, err)
		}
	})
	if started {
		b.debugf("scheduled QUIC reconnect: remote=%s delay=%s", remote, delay)
		return
	}
	ep.mu.Lock()
	if ep.reconnectGeneration == generation {
		ep.cancelReconnectLocked()
	}
	ep.mu.Unlock()
}

func reconnectBackoff(minimum, maximum time.Duration, exponent uint32) time.Duration {
	value := minimum
	for range exponent {
		if value >= maximum/2 {
			return maximum
		}
		value *= 2
	}
	return min(value, maximum)
}

func (b *Bind) hasEstablishedSession(state *runState, remote netip.AddrPort) bool {
	state.mu.Lock()
	sessions := make([]*session, 0, 1)
	for _, candidate := range state.sessions {
		if candidate.endpoint.addr == remote &&
			!candidate.closed.Load() && candidate.ctx.Err() == nil {
			sessions = append(sessions, candidate)
		}
	}
	state.mu.Unlock()
	for _, candidate := range sessions {
		candidate.mu.Lock()
		established := candidate.conn != nil
		candidate.mu.Unlock()
		if established {
			return true
		}
	}
	return false
}

func (b *Bind) suspendConfiguredReconnect(state *runState, remote netip.AddrPort) {
	state.mu.Lock()
	ep := state.endpoints[remote]
	state.mu.Unlock()
	if ep == nil {
		return
	}
	ep.mu.Lock()
	ep.consecutiveFailures = 0
	ep.cancelReconnectLocked()
	ep.mu.Unlock()
}

func (b *Bind) resumeConfiguredReconnect(state *runState, remote netip.AddrPort) {
	state.mu.Lock()
	ep := state.endpoints[remote]
	state.mu.Unlock()
	if ep != nil {
		b.scheduleEndpointReconnect(state, ep, true)
	}
}

func (b *Bind) Send(bufs [][]byte, endpoint conn.Endpoint) error {
	ep, ok := endpoint.(*Endpoint)
	if !ok || ep.owner != b {
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return net.ErrClosed
	}
	sess, err := b.sessionForEndpoint(state, ep, false)
	if err != nil {
		return err
	}
	for _, buf := range bufs {
		if len(buf) == 0 || len(buf) > maxDatagramSize {
			return fmt.Errorf("invalid WireGuard datagram size %d", len(buf))
		}
		b.stats.wgTxPackets.Add(1)
		b.stats.wgTxBytes.Add(uint64(len(buf)))
		queue := sess.send
		if priorityWireGuardDatagram(buf) {
			queue = sess.priority
		}
		preparedFrame := quiccarrier.AcquireDatagramSendBuffer(frameHeaderSize + len(buf))
		copy(preparedFrame[frameHeaderSize:], buf)
		packet := outboundPacket{
			preparedFrame: preparedFrame,
			id:            b.nextPacket.Add(1),
		}
		select {
		case queue <- packet:
		case <-state.ctx.Done():
			quiccarrier.ReleaseDatagramSendBuffer(preparedFrame)
			return net.ErrClosed
		default:
			quiccarrier.ReleaseDatagramSendBuffer(preparedFrame)
			b.stats.queueDrops.Add(1)
			b.debugf("send queue full: session=%d endpoint=%s", sess.id, sess.endpoint.addr)
			return errors.New("wg-quic send queue is full")
		}
	}
	return nil
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
			if len(packets[n]) < len(packet.data) {
				return fmt.Errorf("receive buffer is %d bytes, need %d", len(packets[n]), len(packet.data))
			}
			sizes[n] = copy(packets[n], packet.data)
			if packet.release != nil {
				packet.release(packet.data)
			}
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

func (b *Bind) acceptLoop(state *runState) {
	defer state.wg.Done()
	for {
		qconn, remote, err := state.carrier.Accept(state.ctx)
		if err != nil {
			if state.ctx.Err() == nil {
				b.debugf("accept QUIC session failed: %v", err)
			}
			return
		}
		// Keep an accepted QUIC connection's endpoint identity separate from a
		// configured outbound endpoint for the same address. If both peers dial
		// simultaneously, replacing each configured endpoint's session with the
		// accepted session makes both sides cancel the connection that the other
		// side just selected. A connection-scoped endpoint also makes WireGuard
		// replies use the exact authenticated path that delivered the packet.
		ep := &Endpoint{owner: b, addr: remote}
		state.mu.Lock()
		if state.closing || state.ctx.Err() != nil {
			state.mu.Unlock()
			qconn.CloseWithError("")
			return
		}
		ep.fallback = state.endpoints[remote]
		ep.mu.Lock()
		sess := b.newSessionLocked(state, ep, false)
		ep.session = sess
		ep.mu.Unlock()
		state.wg.Add(1)
		state.mu.Unlock()
		sess.setConn(qconn)
		b.suspendConfiguredReconnect(state, remote)
		b.eventf("accepted QUIC session: session=%d remote=%s", sess.id, remote)
		go func() { defer state.wg.Done(); b.runSession(sess) }()
	}
}

func (b *Bind) sessionForEndpoint(
	state *runState,
	ep *Endpoint,
	reconnectAttempt bool,
) (*session, error) {
	state.mu.Lock()
	if state.closing || state.ctx.Err() != nil {
		state.mu.Unlock()
		return nil, net.ErrClosed
	}
	ep.mu.Lock()
	if ep.session != nil && ep.session.state == state && !ep.session.closed.Load() {
		sess := ep.session
		ep.mu.Unlock()
		state.mu.Unlock()
		return sess, nil
	}
	if !ep.configured {
		fallback := ep.fallback
		ep.mu.Unlock()
		state.mu.Unlock()
		if fallback != nil {
			return b.sessionForEndpoint(state, fallback, reconnectAttempt)
		}
		return nil, errors.New("cannot dial a connection-scoped peer endpoint")
	}
	if ep.retired {
		ep.mu.Unlock()
		state.mu.Unlock()
		return nil, errors.New("cannot dial a retired peer endpoint")
	}
	if state.endpoints[ep.addr] == nil {
		state.endpoints[ep.addr] = ep
	}
	ep.activated = true
	ep.cancelReconnectLocked()
	sess := b.newSessionLocked(state, ep, reconnectAttempt)
	ep.session = sess
	if reconnectAttempt {
		ep.reconnectAttempts++
	}
	state.wg.Add(1)
	ep.mu.Unlock()
	state.mu.Unlock()
	go b.dialSession(sess)
	return sess, nil
}

// newSessionLocked constructs and registers a session while state.mu is held.
// Callers use the state -> endpoint lock order shared with ParseEndpoint.
func (b *Bind) newSessionLocked(
	state *runState,
	ep *Endpoint,
	reconnectAttempt bool,
) *session {
	ctx, cancel := context.WithCancel(state.ctx)
	sess := &session{
		id: b.nextSession.Add(1), state: state, endpoint: ep, ctx: ctx, cancel: cancel,
		ready: make(chan struct{}), send: make(chan outboundPacket, state.cfg.QueueSize),
		priority: make(chan outboundPacket, max(64, state.cfg.QueueSize/8)),
		control:  make(chan []byte, 64), reconnectAttempt: reconnectAttempt,
		remoteAddr: ep.addr,
	}
	sess.fecDecoder = fec.NewDecoder()
	if state.cfg.FECMode == "auto" {
		sess.fecEncoder = fec.NewEncoder(state.cfg.FECDataShards, fec.NewController())
		// SetInterleave cannot fail at construction: no groups are in flight.
		_ = sess.fecEncoder.SetInterleave(state.cfg.FECInterleave)
	}
	state.sessions[sess.id] = sess
	b.stats.activeSessions.Add(1)
	return sess
}

func (b *Bind) dialSession(sess *session) {
	defer sess.state.wg.Done()
	b.debugf("dialing QUIC session: session=%d remote=%s", sess.id, sess.endpoint.addr)
	ctx, cancel := context.WithTimeout(sess.ctx, sess.state.cfg.HandshakeTimeout)
	defer cancel()
	qconn, err := sess.state.carrier.Dial(ctx, sess.endpoint.addr)
	if err != nil {
		sess.endpoint.mu.Lock()
		attempt := sess.endpoint.reconnectAttempts
		if sess.endpoint.configured && sess.endpoint.session == sess {
			sess.endpoint.consecutiveFailures++
			if sess.reconnectAttempt {
				sess.endpoint.reconnectFailures++
			}
		}
		sess.endpoint.mu.Unlock()
		if !sess.reconnectAttempt {
			b.eventf(
				"QUIC dial failed: session=%d remote=%s error=%v",
				sess.id, sess.endpoint.addr, err,
			)
		} else if reportReconnectAttempt(attempt) {
			b.eventf(
				"QUIC reconnect failed: session=%d remote=%s attempt=%d error=%v",
				sess.id, sess.endpoint.addr, attempt, err,
			)
		} else {
			b.debugf(
				"QUIC reconnect failed: session=%d remote=%s attempt=%d error=%v",
				sess.id, sess.endpoint.addr, attempt, err,
			)
		}
		sess.close()
		return
	}
	sess.setConn(qconn)
	b.eventf("QUIC session established: session=%d remote=%s", sess.id, sess.endpoint.addr)
	if sess.reconnectAttempt {
		b.mu.Lock()
		restored := b.sessionRestored
		b.mu.Unlock()
		if restored != nil {
			restored(sess.endpoint.addr)
		}
	}
	b.runSession(sess)
}

func reportReconnectAttempt(attempt uint64) bool {
	return attempt != 0 && attempt&(attempt-1) == 0
}

func (b *Bind) runSession(sess *session) {
	defer sess.close()
	sendDone := make(chan struct{})
	go func() { defer close(sendDone); sess.sendLoop() }()
	sess.receiveLoop()
	sess.cancel()
	<-sendDone
}

type session struct {
	id                  uint64
	state               *runState
	endpoint            *Endpoint
	ctx                 context.Context
	cancel              context.CancelFunc
	ready               chan struct{}
	send                chan outboundPacket
	priority            chan outboundPacket
	control             chan []byte
	mu                  sync.Mutex
	conn                *quiccarrier.Connection
	remoteAddr          netip.AddrPort
	establishedAt       time.Time
	readyOnce           sync.Once
	closeOnce           sync.Once
	closed              atomic.Bool
	reconnectAttempt    bool
	fecEncoder          *fec.Encoder
	fecDecoder          *fec.Decoder
	fecPathSampleFrames uint32
}

func (s *session) setConn(qconn *quiccarrier.Connection) {
	s.mu.Lock()
	s.conn = qconn
	s.establishedAt = time.Now()
	s.mu.Unlock()
	s.endpoint.mu.Lock()
	if s.endpoint.configured {
		s.endpoint.nextReconnect = time.Time{}
	}
	s.endpoint.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.cancel()
		s.mu.Lock()
		establishedAt := s.establishedAt
		if s.conn != nil {
			s.conn.CloseWithError("")
		}
		s.mu.Unlock()
		s.state.mu.Lock()
		delete(s.state.sessions, s.id)
		s.state.mu.Unlock()
		s.endpoint.owner.stats.activeSessions.Add(^uint64(0))
		s.endpoint.mu.Lock()
		configured := s.endpoint.configured
		current := s.endpoint.session == s
		if current {
			s.endpoint.session = nil
			if configured && !establishedAt.IsZero() {
				if time.Since(establishedAt) < s.state.cfg.ReconnectStable {
					s.endpoint.consecutiveFailures++
					if s.reconnectAttempt {
						s.endpoint.reconnectFailures++
					}
				} else {
					s.endpoint.consecutiveFailures = 0
				}
			}
		}
		s.endpoint.mu.Unlock()
		if !s.reconnectAttempt || !establishedAt.IsZero() {
			s.endpoint.owner.eventf("QUIC session closed: session=%d remote=%s", s.id, s.endpoint.addr)
		} else {
			s.endpoint.owner.debugf("failed QUIC reconnect closed: session=%d remote=%s", s.id, s.endpoint.addr)
		}
		if configured {
			s.endpoint.owner.scheduleEndpointReconnect(s.state, s.endpoint, false)
		} else {
			s.endpoint.owner.resumeConfiguredReconnect(s.state, s.endpoint.addr)
		}
	})
}

func (s *session) sendLoop() {
	select {
	case <-s.ready:
	case <-s.ctx.Done():
		return
	}
	s.mu.Lock()
	qconn := s.conn
	s.mu.Unlock()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	timerActive := false
	defer timer.Stop()

	sendPacket := func(packet []byte) bool {
		packetBytes := len(packet)
		kind, fecPacket := fec.PacketKind(packet)
		if err := qconn.SendDatagramOwned(packet); err != nil {
			return false
		}
		s.endpoint.owner.stats.wireTxPackets.Add(1)
		s.endpoint.owner.stats.wireTxBytes.Add(uint64(packetBytes))
		if fecPacket {
			switch kind {
			case fec.KindData:
				s.endpoint.owner.stats.fecDataTx.Add(1)
			case fec.KindParity:
				s.endpoint.owner.stats.fecParityTx.Add(1)
			}
		}
		return true
	}
	sendPackets := func(packets [][]byte) bool {
		for _, packet := range packets {
			if !sendPacket(packet) {
				return false
			}
		}
		return true
	}
	stopTimer := func() {
		if timerActive && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerActive = false
	}
	resetTimer := func() {
		stopTimer()
		timer.Reset(s.state.cfg.FECFlushDeadline)
		timerActive = true
	}
	sendFrame := func(frame []byte) bool {
		if s.fecEncoder == nil {
			return sendPacket(frame)
		}
		s.fecPathSampleFrames++
		if s.fecPathSampleFrames%32 == 0 {
			stats := qconn.Stats()
			s.fecEncoder.ObservePathRTT(stats.PropagationRTT)
			s.fecEncoder.ObserveTransport(stats.PacketsSent, stats.PacketsLost)
		}
		packets, err := s.fecEncoder.Add(frame)
		if err != nil || !sendPackets(packets) {
			return false
		}
		if s.fecEncoder.Pending() {
			if !timerActive {
				resetTimer()
			}
		} else {
			stopTimer()
		}
		return true
	}
	sendWGPacket := func(packet outboundPacket) bool {
		fragmentData := qconn.MaxDatagramPayloadSize() - frameHeaderSize
		if s.fecEncoder != nil {
			fragmentData -= fec.DataPacketOverhead
		}
		fragmentData = min(fragmentData, maxFragmentData)
		payload := packet.preparedFrame[frameHeaderSize:]
		if len(payload) <= fragmentData {
			frame, err := framePreparedPacket(packet.preparedFrame, packet.id)
			return err == nil && sendFrame(frame)
		}
		frames, err := fragmentPacketSized(payload, packet.id, fragmentData)
		if err != nil {
			return false
		}
		for _, frame := range frames {
			if !sendFrame(frame) {
				return false
			}
		}
		return true
	}

	for {
		select {
		case control := <-s.control:
			if !sendPacket(control) {
				return
			}
			continue
		default:
		}
		select {
		case packet := <-s.priority:
			if !sendWGPacket(packet) {
				return
			}
			continue
		default:
		}
		select {
		case packet := <-s.priority:
			if !sendWGPacket(packet) {
				return
			}
		case packet := <-s.send:
			if !sendWGPacket(packet) {
				return
			}
		case control := <-s.control:
			if !sendPacket(control) {
				return
			}
		case <-timer.C:
			timerActive = false
			if s.fecEncoder != nil {
				packets, err := s.fecEncoder.Flush()
				if err != nil || !sendPackets(packets) {
					return
				}
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *session) receiveLoop() {
	select {
	case <-s.ready:
	case <-s.ctx.Done():
		return
	}
	s.mu.Lock()
	qconn := s.conn
	s.mu.Unlock()
	type receiveResult struct {
		datagram quiccarrier.ReceivedDatagram
		err      error
	}
	// Buffer the handoff so a processing burst never stalls the goroutine
	// blocked in ReceiveDatagramOwned; 64 covers a full QUIC batch with
	// headroom without queuing unbounded datagrams.
	incoming := make(chan receiveResult, 64)
	go func() {
		for {
			datagram, err := qconn.ReceiveDatagramOwned(s.ctx)
			select {
			case incoming <- receiveResult{datagram: datagram, err: err}:
			case <-s.ctx.Done():
				datagram.Release()
				return
			}
			if err != nil {
				return
			}
		}
	}()
	handleDatagram := func(datagram quiccarrier.ReceivedDatagram) {
		defer datagram.Release()
		wirePacket := datagram.Data
		s.mu.Lock()
		s.remoteAddr = datagram.RemoteAddr
		s.mu.Unlock()
		receiveEndpoint := s.endpointForAddrPort(datagram.RemoteAddr)
		s.endpoint.owner.stats.wireRxPackets.Add(1)
		s.endpoint.owner.stats.wireRxBytes.Add(uint64(len(wirePacket)))
		result, err := s.fecDecoder.Handle(time.Now(), wirePacket)
		if err != nil {
			return
		}
		if result.Handled {
			if result.ObservedFeedback != nil && s.fecEncoder != nil {
				s.fecEncoder.Observe(*result.ObservedFeedback)
				qconn.ObserveFECFeedback(
					uint64(result.ObservedFeedback.Total),
					uint64(result.ObservedFeedback.Missing),
					uint64(result.ObservedFeedback.Recovered),
				)
			}
			s.sendFECFeedback(result.SendFeedback)
			for _, frame := range result.Frames {
				s.deliverFrame(frame, true, receiveEndpoint)
			}
			return
		}
		s.deliverFrame(wirePacket, false, receiveEndpoint)
	}
	expiry := time.NewTicker(fecExpiryPoll)
	defer expiry.Stop()
	for {
		select {
		case received := <-incoming:
			if received.err != nil {
				received.datagram.Release()
				if s.ctx.Err() == nil {
					s.endpoint.owner.debugf(
						"QUIC receive stopped: session=%d remote=%s error=%v",
						s.id, s.endpoint.addr, received.err,
					)
				}
				return
			}
			handleDatagram(received.datagram)
		case now := <-expiry.C:
			s.sendFECFeedback(s.fecDecoder.Expire(now))
			continue
		case <-s.ctx.Done():
			return
		}
	}
}

// endpointForRemote snapshots the live QUIC path for the WireGuard packet
// being delivered. quic-go can migrate an established connection after a NAT
// rebinding, while the endpoint created by Accept retains the address of the
// initial path. WireGuard must receive the new immutable address so an
// authenticated packet updates its roaming state and status output without
// changing the session's configured/reconnect identity.
func (s *session) endpointForAddrPort(remote netip.AddrPort) *Endpoint {
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	if remote == s.endpoint.addr {
		return s.endpoint
	}
	s.endpoint.mu.Lock()
	fallback := s.endpoint.fallback
	if s.endpoint.configured {
		fallback = s.endpoint
	}
	s.endpoint.mu.Unlock()
	return &Endpoint{
		owner:    s.endpoint.owner,
		addr:     remote,
		session:  s,
		fallback: fallback,
	}
}

func (s *session) sendFECFeedback(feedbacks []fec.Feedback) {
	for _, feedback := range feedbacks {
		s.endpoint.owner.stats.fecRawLost.Add(uint64(feedback.Missing))
		s.endpoint.owner.stats.fecRecovered.Add(uint64(feedback.Recovered))
		if feedback.Missing > feedback.Recovered {
			s.endpoint.owner.stats.fecUnrecovered.Add(uint64(feedback.Missing - feedback.Recovered))
		}
		select {
		case s.control <- fec.MarshalFeedback(feedback):
		default:
		}
	}
}

// deliverFrame parses and reassembles one WGQ1 frame. owned reports whether
// frame stays valid after this call returns (FEC decoder output) as opposed
// to aliasing the pooled QUIC receive datagram.
func (s *session) deliverFrame(frame []byte, owned bool, endpoint *Endpoint) {
	frag, err := parseFragment(frame)
	if err != nil {
		return
	}
	s.state.mu.Lock()
	packet, err := s.state.reassembly.add(time.Now(), s.id, frag, owned)
	s.state.mu.Unlock()
	if err != nil || packet == nil {
		return
	}
	s.endpoint.owner.stats.wgRxPackets.Add(1)
	s.endpoint.owner.stats.wgRxBytes.Add(uint64(len(packet)))
	rp := receivedPacket{data: packet, ep: endpoint}
	if !(owned && frag.count == 1) {
		// Everything except an owned single-fragment passthrough is a buffer
		// this package allocated; return it to the pool once the receive
		// worker has copied it out. Foreign capacities no-op inside release.
		rp.release = releaseReassemblyBuffer
	}
	select {
	case s.state.recv <- rp:
	case <-s.ctx.Done():
		if rp.release != nil {
			rp.release(rp.data)
		}
	default:
		s.endpoint.owner.stats.queueDrops.Add(1)
		if rp.release != nil {
			rp.release(rp.data)
		}
	}
}
