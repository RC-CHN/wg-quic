package armorbind

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/fec"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/conn"
	"github.com/quic-go/quic-go"
)

const alpn = "wg-quic/1"

const fecExpiryPoll = 500 * time.Millisecond

type Config struct {
	QueueSize        int
	HandshakeTimeout time.Duration
	MaxIdleTimeout   time.Duration
	KeepAlivePeriod  time.Duration
	FECMode          string
	FECDataShards    int
	FECFlushDeadline time.Duration
	ObfsMode         string
	ObfsKeys         []obfs.Key
	ObfsEndpointKeys map[string]obfs.Key
}

func DefaultConfig() Config {
	return Config{
		QueueSize:        1024,
		HandshakeTimeout: 4 * time.Second,
		MaxIdleTimeout:   15 * time.Second,
		KeepAlivePeriod:  5 * time.Second,
		FECMode:          "auto", FECDataShards: fec.DefaultDataShards, FECFlushDeadline: 2 * time.Millisecond, ObfsMode: "none",
	}
}

type receivedPacket struct {
	data []byte
	ep   *Endpoint
}

type runState struct {
	ctx        context.Context
	cancel     context.CancelFunc
	listener   *quic.Listener
	transport  *quic.Transport
	packetConn net.PacketConn
	obfsConn   *obfs.SalamanderConn
	recv       chan receivedPacket
	cfg        Config
	mu         sync.Mutex
	sessions   map[uint64]*session
	endpoints  map[netip.AddrPort]*Endpoint
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

type Stats struct {
	WGTxPackets    uint64 `json:"wg_tx_packets"`
	WGTxBytes      uint64 `json:"wg_tx_bytes"`
	WGRxPackets    uint64 `json:"wg_rx_packets"`
	WGRxBytes      uint64 `json:"wg_rx_bytes"`
	WireTxPackets  uint64 `json:"wire_tx_packets"`
	WireTxBytes    uint64 `json:"wire_tx_bytes"`
	WireRxPackets  uint64 `json:"wire_rx_packets"`
	WireRxBytes    uint64 `json:"wire_rx_bytes"`
	QueueDrops     uint64 `json:"queue_drops"`
	FECDataTx      uint64 `json:"fec_data_tx"`
	FECParityTx    uint64 `json:"fec_parity_tx"`
	FECRawLost     uint64 `json:"fec_raw_lost"`
	FECRecovered   uint64 `json:"fec_recovered"`
	FECUnrecovered uint64 `json:"fec_unrecovered"`
	ActiveSessions uint64 `json:"active_sessions"`
}

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
	endpointKeys := make(map[string]obfs.Key, len(cfg.ObfsEndpointKeys))
	for endpoint, key := range cfg.ObfsEndpointKeys {
		endpointKeys[endpoint] = key
	}
	cfg.ObfsEndpointKeys = endpointKeys
	return &Bind{cfg: cfg, obfsResolved: make(map[netip.AddrPort]obfs.Key)}
}

func (b *Bind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	tlsConfig, err := serverTLSConfig()
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rawConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: int(port)})
	if err != nil {
		cancel()
		return nil, 0, err
	}
	if mark := b.mark.Load(); mark != 0 {
		if err := setSocketMark(rawConn, mark); err != nil {
			rawConn.Close()
			cancel()
			return nil, 0, err
		}
	}
	var transportConn net.PacketConn = rawConn
	var obfsConn *obfs.SalamanderConn
	switch b.cfg.ObfsMode {
	case "none":
	case "salamander":
		peers := make([]obfs.PeerKey, 0, len(b.cfg.ObfsKeys)+len(b.obfsResolved))
		for _, key := range b.cfg.ObfsKeys {
			peers = append(peers, obfs.PeerKey{Key: key})
		}
		for endpoint, key := range b.obfsResolved {
			peers = append(peers, obfs.PeerKey{Key: key, Endpoint: endpoint})
		}
		obfsConn, err = obfs.WrapKeyedSalamander(rawConn, peers)
		if err != nil {
			rawConn.Close()
			cancel()
			return nil, 0, err
		}
	default:
		rawConn.Close()
		cancel()
		return nil, 0, fmt.Errorf("unsupported obfuscation mode %q", b.cfg.ObfsMode)
	}
	transport := &quic.Transport{Conn: transportConn}
	listener, err := transport.Listen(tlsConfig, quicConfig(b.cfg))
	if err != nil {
		transport.Close()
		cancel()
		return nil, 0, err
	}
	_, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		transport.Close()
		cancel()
		return nil, 0, err
	}
	actualPort, err := strconv.ParseUint(portString, 10, 16)
	if err != nil {
		listener.Close()
		transport.Close()
		cancel()
		return nil, 0, err
	}
	state := &runState{
		ctx: ctx, cancel: cancel, listener: listener, transport: transport, packetConn: rawConn, obfsConn: obfsConn,
		recv: make(chan receivedPacket, b.cfg.QueueSize), cfg: b.cfg,
		sessions: make(map[uint64]*session), endpoints: make(map[netip.AddrPort]*Endpoint), reassembly: newReassembler(),
	}
	b.state = state
	state.wg.Add(1)
	go b.acceptLoop(state)
	return []conn.ReceiveFunc{b.receiveFunc(state)}, uint16(actualPort), nil
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
	listenerErr := state.listener.Close()
	state.mu.Lock()
	for _, sess := range state.sessions {
		sess.cancel()
	}
	state.mu.Unlock()
	transportErr := state.transport.Close()
	state.wg.Wait()
	if listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) {
		return listenerErr
	}
	if transportErr != nil && !errors.Is(transportErr, net.ErrClosed) {
		return transportErr
	}
	return nil
}

func (b *Bind) SetMark(mark uint32) error {
	b.mark.Store(mark)
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return nil
	}
	return setSocketMark(state.packetConn, mark)
}
func (b *Bind) BatchSize() int { return 32 }

func (b *Bind) Stats() Stats {
	return Stats{
		WGTxPackets: b.stats.wgTxPackets.Load(), WGTxBytes: b.stats.wgTxBytes.Load(),
		WGRxPackets: b.stats.wgRxPackets.Load(), WGRxBytes: b.stats.wgRxBytes.Load(),
		WireTxPackets: b.stats.wireTxPackets.Load(), WireTxBytes: b.stats.wireTxBytes.Load(),
		WireRxPackets: b.stats.wireRxPackets.Load(), WireRxBytes: b.stats.wireRxBytes.Load(),
		QueueDrops: b.stats.queueDrops.Load(), FECDataTx: b.stats.fecDataTx.Load(),
		FECParityTx: b.stats.fecParityTx.Load(), FECRawLost: b.stats.fecRawLost.Load(),
		FECRecovered: b.stats.fecRecovered.Load(), FECUnrecovered: b.stats.fecUnrecovered.Load(),
		ActiveSessions: b.stats.activeSessions.Load(),
	}
}

func (b *Bind) Port() uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == nil {
		return 0
	}
	_, port, err := net.SplitHostPort(b.state.listener.Addr().String())
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(port, 10, 16)
	return uint16(v)
}

func (b *Bind) ParseEndpoint(value string) (conn.Endpoint, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", value)
	if err != nil {
		return nil, err
	}
	addr, ok := netip.AddrFromSlice(udpAddr.IP)
	if !ok {
		return nil, fmt.Errorf("endpoint %q did not resolve to an IP address", value)
	}
	ep := &Endpoint{owner: b, addr: netip.AddrPortFrom(addr.Unmap(), uint16(udpAddr.Port))}
	b.mu.Lock()
	if key, ok := b.cfg.ObfsEndpointKeys[value]; ok {
		b.obfsResolved[ep.addr] = key
		if b.state != nil && b.state.obfsConn != nil {
			b.state.obfsConn.AssociateEndpoint(ep.addr, key)
		}
	}
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
	return ep, nil
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
		b.stats.wgTxPackets.Add(1)
		b.stats.wgTxBytes.Add(uint64(len(buf)))
		queue := sess.send
		if priorityWireGuardDatagram(buf) {
			queue = sess.priority
		}
		frames, err := fragmentPacket(buf, b.nextPacket.Add(1))
		if err != nil {
			return err
		}
		for _, frame := range frames {
			select {
			case queue <- frame:
			case <-state.ctx.Done():
				return net.ErrClosed
			default:
				b.stats.queueDrops.Add(1)
				return errors.New("wg-quic send queue is full")
			}
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
		qconn, err := state.listener.Accept(state.ctx)
		if err != nil {
			return
		}
		remote, err := addrPort(qconn.RemoteAddr())
		if err != nil {
			qconn.CloseWithError(1, err.Error())
			continue
		}
		ep := b.endpointFor(state, remote)
		sess := b.newSession(state, ep)
		sess.setConn(qconn)
		b.installSession(ep, sess)
		state.wg.Add(1)
		go func() { defer state.wg.Done(); b.runSession(sess) }()
	}
}

func (b *Bind) sessionForEndpoint(state *runState, ep *Endpoint) *session {
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

func (b *Bind) installSession(ep *Endpoint, sess *session) {
	ep.mu.Lock()
	old := ep.session
	ep.session = sess
	ep.mu.Unlock()
	if old != nil && old != sess {
		old.cancel()
	}
}

func (b *Bind) endpointFor(state *runState, remote netip.AddrPort) *Endpoint {
	state.mu.Lock()
	defer state.mu.Unlock()
	if ep := state.endpoints[remote]; ep != nil {
		return ep
	}
	ep := &Endpoint{owner: b, addr: remote}
	state.endpoints[remote] = ep
	return ep
}

func (b *Bind) newSession(state *runState, ep *Endpoint) *session {
	ctx, cancel := context.WithCancel(state.ctx)
	sess := &session{
		id: b.nextSession.Add(1), state: state, endpoint: ep, ctx: ctx, cancel: cancel,
		ready: make(chan struct{}), send: make(chan []byte, state.cfg.QueueSize),
		priority: make(chan []byte, max(64, state.cfg.QueueSize/8)),
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
	ctx, cancel := context.WithTimeout(sess.ctx, sess.state.cfg.HandshakeTimeout)
	defer cancel()
	qconn, err := sess.state.transport.Dial(ctx, net.UDPAddrFromAddrPort(sess.endpoint.addr), &tls.Config{
		InsecureSkipVerify: true, NextProtos: []string{alpn}, MinVersion: tls.VersionTLS13,
	}, quicConfig(sess.state.cfg))
	if err != nil {
		sess.close()
		return
	}
	sess.setConn(qconn)
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
	id         uint64
	state      *runState
	endpoint   *Endpoint
	ctx        context.Context
	cancel     context.CancelFunc
	ready      chan struct{}
	send       chan []byte
	priority   chan []byte
	control    chan []byte
	mu         sync.Mutex
	conn       *quic.Conn
	readyOnce  sync.Once
	closeOnce  sync.Once
	closed     atomic.Bool
	fecEncoder *fec.Encoder
	fecDecoder *fec.Decoder
}

func (s *session) setConn(qconn *quic.Conn) {
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
			s.conn.CloseWithError(0, "")
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
		case frame := <-s.priority:
			if !sendFrame(frame) {
				return
			}
			continue
		default:
		}
		select {
		case frame := <-s.priority:
			if !sendFrame(frame) {
				return
			}
		case frame := <-s.send:
			if !sendFrame(frame) {
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

func quicConfig(cfg Config) *quic.Config {
	return &quic.Config{
		EnableDatagrams: true, HandshakeIdleTimeout: cfg.HandshakeTimeout, MaxIdleTimeout: cfg.MaxIdleTimeout,
		KeepAlivePeriod: cfg.KeepAlivePeriod, MaxIncomingStreams: -1, MaxIncomingUniStreams: -1,
	}
}

func serverTLSConfig() (*tls.Config, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, privateKey.Public(), privateKey)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{certDER}, PrivateKey: privateKey}},
		NextProtos:   []string{alpn}, MinVersion: tls.VersionTLS13,
	}, nil
}

func addrPort(addr net.Addr) (netip.AddrPort, error) {
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("unexpected remote address %T", addr)
	}
	ip, ok := netip.AddrFromSlice(udp.IP)
	if !ok {
		return netip.AddrPort{}, errors.New("remote address has invalid IP")
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(udp.Port)), nil
}
