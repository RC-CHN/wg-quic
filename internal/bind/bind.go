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
	CongestionMode   string
	FECMode          string
	FECDataShards    int
	FECFlushDeadline time.Duration
	ObfsMode         string
	ObfsKeys         []obfs.Key
	Debugf           func(format string, args ...any)
}

func DefaultConfig() Config {
	return Config{
		QueueSize:        1024,
		HandshakeTimeout: 4 * time.Second,
		MaxIdleTimeout:   15 * time.Second,
		KeepAlivePeriod:  5 * time.Second,
		CongestionMode:   "model",
		FECMode:          "auto", FECDataShards: fec.DefaultDataShards, FECFlushDeadline: 2 * time.Millisecond, ObfsMode: "none",
	}
}

type receivedPacket struct {
	data []byte
	ep   *Endpoint
}

type outboundPacket struct {
	data []byte
	id   uint64
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

	reassembly *reassembler
	wg         sync.WaitGroup
}

type Bind struct {
	cfg          Config
	mu           sync.Mutex
	state        *runState
	nextPacket   atomic.Uint64
	nextSession  atomic.Uint64
	mark         atomic.Uint32
	stats        bindStats
	obfsResolved map[netip.AddrPort]obfs.Key
	obfsDynamic  map[netip.AddrPort]endpointKeyLease
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

const (
	EndpointSessionIdle        EndpointSessionState = "idle"
	EndpointSessionDialing     EndpointSessionState = "dialing"
	EndpointSessionEstablished EndpointSessionState = "established"
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
	if cfg.CongestionMode == "" || cfg.CongestionMode == "auto" {
		cfg.CongestionMode = defaults.CongestionMode
	}
	if cfg.FECMode == "" {
		cfg.FECMode = defaults.FECMode
	}
	if cfg.FECDataShards <= 0 {
		cfg.FECDataShards = defaults.FECDataShards
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
	state.cancel()
	state.mu.Lock()
	for _, sess := range state.sessions {
		sess.cancel()
	}
	state.mu.Unlock()
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
	ep := state.endpoints[endpoint]
	state.mu.Unlock()
	if ep == nil {
		return EndpointSessionIdle
	}
	ep.mu.Lock()
	session := ep.session
	ep.mu.Unlock()
	if session == nil || session.closed.Load() {
		return EndpointSessionIdle
	}
	session.mu.Lock()
	established := session.conn != nil
	session.mu.Unlock()
	if established {
		return EndpointSessionEstablished
	}
	return EndpointSessionDialing
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
	b.mu.Lock()
	_, associated := b.obfsResolved[ep.addr]
	state := b.state
	b.mu.Unlock()
	if state != nil {
		state.mu.Lock()
		if existing := state.endpoints[ep.addr]; existing != nil {
			ep = existing
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
	retireEndpointSession(ep)
}

// RedialEndpoint closes the current configured session while retaining the
// endpoint object. The next WireGuard send creates a fresh QUIC session.
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
	retireEndpointSession(ep)
}

func retireEndpointSession(endpoint *Endpoint) {
	if endpoint == nil {
		return
	}
	endpoint.mu.Lock()
	session := endpoint.session
	endpoint.session = nil
	endpoint.mu.Unlock()
	if session != nil {
		session.cancel()
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
	sess := b.sessionForEndpoint(state, ep)
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
		packet := outboundPacket{
			data: append([]byte(nil), buf...),
			id:   b.nextPacket.Add(1),
		}
		select {
		case queue <- packet:
		case <-state.ctx.Done():
			return net.ErrClosed
		default:
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
		sess := b.newSession(state, ep)
		ep.session = sess
		sess.setConn(qconn)
		b.debugf("accepted QUIC session: session=%d remote=%s", sess.id, remote)
		state.wg.Add(1)
		go func() { defer state.wg.Done(); b.runSession(sess) }()
	}
}

func (b *Bind) sessionForEndpoint(state *runState, ep *Endpoint) *session {
	state.mu.Lock()
	if state.endpoints[ep.addr] == nil {
		state.endpoints[ep.addr] = ep
	}
	state.mu.Unlock()
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.session != nil && ep.session.state == state && !ep.session.closed.Load() {
		return ep.session
	}
	sess := b.newSession(state, ep)
	ep.session = sess
	state.wg.Add(1)
	go b.dialSession(sess)
	return sess
}

func (b *Bind) newSession(state *runState, ep *Endpoint) *session {
	ctx, cancel := context.WithCancel(state.ctx)
	sess := &session{
		id: b.nextSession.Add(1), state: state, endpoint: ep, ctx: ctx, cancel: cancel,
		ready: make(chan struct{}), send: make(chan outboundPacket, state.cfg.QueueSize),
		priority: make(chan outboundPacket, max(64, state.cfg.QueueSize/8)),
		control:  make(chan []byte, 64),
	}
	sess.fecDecoder = fec.NewDecoder()
	if state.cfg.FECMode == "auto" {
		sess.fecEncoder = fec.NewEncoder(state.cfg.FECDataShards, fec.NewController())
	}
	state.mu.Lock()
	state.sessions[sess.id] = sess
	state.mu.Unlock()
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
		b.debugf("QUIC dial failed: session=%d remote=%s error=%v", sess.id, sess.endpoint.addr, err)
		sess.close()
		return
	}
	sess.setConn(qconn)
	b.debugf("QUIC session established: session=%d remote=%s", sess.id, sess.endpoint.addr)
	b.runSession(sess)
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
	readyOnce           sync.Once
	closeOnce           sync.Once
	closed              atomic.Bool
	fecEncoder          *fec.Encoder
	fecDecoder          *fec.Decoder
	fecPathSampleFrames uint32
}

func (s *session) setConn(qconn *quiccarrier.Connection) {
	s.mu.Lock()
	s.conn = qconn
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.cancel()
		s.mu.Lock()
		if s.conn != nil {
			s.conn.CloseWithError("")
		}
		s.mu.Unlock()
		s.state.mu.Lock()
		delete(s.state.sessions, s.id)
		s.state.mu.Unlock()
		s.endpoint.owner.stats.activeSessions.Add(^uint64(0))
		s.endpoint.mu.Lock()
		if s.endpoint.session == s {
			s.endpoint.session = nil
		}
		s.endpoint.mu.Unlock()
		s.endpoint.owner.debugf("QUIC session closed: session=%d remote=%s", s.id, s.endpoint.addr)
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

	sendPackets := func(packets [][]byte) bool {
		for _, packet := range packets {
			if err := qconn.SendDatagram(packet); err != nil {
				return false
			}
			s.endpoint.owner.stats.wireTxPackets.Add(1)
			s.endpoint.owner.stats.wireTxBytes.Add(uint64(len(packet)))
			if kind, ok := fec.PacketKind(packet); ok {
				switch kind {
				case fec.KindData:
					s.endpoint.owner.stats.fecDataTx.Add(1)
				case fec.KindParity:
					s.endpoint.owner.stats.fecParityTx.Add(1)
				}
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
			return sendPackets([][]byte{frame})
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
		frames, err := fragmentPacketSized(packet.data, packet.id, fragmentData)
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
			if !sendPackets([][]byte{control}) {
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
			if !sendPackets([][]byte{control}) {
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
		packet []byte
		err    error
	}
	incoming := make(chan receiveResult)
	go func() {
		for {
			packet, err := qconn.ReceiveDatagram(s.ctx)
			select {
			case incoming <- receiveResult{packet: packet, err: err}:
			case <-s.ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	expiry := time.NewTicker(fecExpiryPoll)
	defer expiry.Stop()
	for {
		var wirePacket []byte
		select {
		case received := <-incoming:
			if received.err != nil {
				if s.ctx.Err() == nil {
					s.endpoint.owner.debugf(
						"QUIC receive stopped: session=%d remote=%s error=%v",
						s.id, s.endpoint.addr, received.err,
					)
				}
				return
			}
			wirePacket = received.packet
		case now := <-expiry.C:
			s.sendFECFeedback(s.fecDecoder.Expire(now))
			continue
		case <-s.ctx.Done():
			return
		}
		s.endpoint.owner.stats.wireRxPackets.Add(1)
		s.endpoint.owner.stats.wireRxBytes.Add(uint64(len(wirePacket)))
		result, err := s.fecDecoder.Handle(time.Now(), wirePacket)
		if err != nil {
			continue
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
				s.deliverFrame(frame)
			}
			continue
		}
		s.deliverFrame(wirePacket)
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

func (s *session) deliverFrame(frame []byte) {
	frag, err := parseFragment(frame)
	if err != nil {
		return
	}
	s.state.mu.Lock()
	packet, err := s.state.reassembly.add(time.Now(), s.id, frag)
	s.state.mu.Unlock()
	if err != nil || packet == nil {
		return
	}
	s.endpoint.owner.stats.wgRxPackets.Add(1)
	s.endpoint.owner.stats.wgRxBytes.Add(uint64(len(packet)))
	select {
	case s.state.recv <- receivedPacket{data: packet, ep: s.endpoint}:
	case <-s.ctx.Done():
	default:
		s.endpoint.owner.stats.queueDrops.Add(1)
	}
}
