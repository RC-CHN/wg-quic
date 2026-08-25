package armorbind

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"runtime"
	"sort"
	"strings"
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

const (
	fecExpiryPoll             = 500 * time.Millisecond
	maxSessionTelemetry       = 256
	maxRecentSessionTelemetry = 64
	recentSessionTelemetryTTL = 5 * time.Minute
	maxSessionEvents          = 4096
	maxSessionEventQuery      = 1024
)

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
	eventMu                sync.Mutex
	eventOrigin            time.Time
	eventStreamID          string
	eventSequence          uint64
	eventsDropped          uint64
	events                 []telemetry.SessionEvent
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

type sessionCounters struct {
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
}

type EndpointSessionState string

type EndpointReconnectStatus struct {
	Attempts            uint64
	Failures            uint64
	ConsecutiveFailures uint32
	NextReconnect       int64
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
	eventOrigin := time.Now()
	var eventStreamBytes [16]byte
	_, _ = cryptorand.Read(eventStreamBytes[:])
	eventStreamID := hex.EncodeToString(eventStreamBytes[:])
	return &Bind{
		cfg: cfg, obfsResolved: make(map[netip.AddrPort]obfs.Key),
		obfsDynamic:  make(map[netip.AddrPort]endpointKeyLease),
		obfsReceive:  make(map[obfs.Key]receiveKeyLease),
		fecPolicies:  make(map[netip.AddrPort]endpointFECPolicyLease),
		replacements: make(map[uint64]uint64),
		eventOrigin:  eventOrigin, eventStreamID: eventStreamID,
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
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state != nil {
		overflow := state.carrier.ReceiveQueueOverflowStats()
		stats.ReceiveQueueOverflow = b.observeReceiveQueueOverflow(state.carrier, overflow)
	} else {
		stats.ReceiveQueueOverflow = telemetry.ReceiveQueueOverflowObservation{
			Source: "unavailable", Platform: runtime.GOOS,
		}
	}
	b.addQUICStats(&stats)
	return stats
}

func (b *Bind) observeReceiveQueueOverflow(
	carrier *quiccarrier.Carrier,
	observation quiccarrier.ReceiveQueueOverflowStats,
) telemetry.ReceiveQueueOverflowObservation {
	result := telemetry.ReceiveQueueOverflowObservation{
		Supported: observation.Supported, Source: observation.Source,
		Platform: runtime.GOOS, Packets: observation.Packets,
	}
	if !observation.Supported {
		return result
	}
	b.receiveOverflowMu.Lock()
	if b.receiveOverflowCarrier != carrier {
		b.receiveOverflowCarrier = carrier
		b.receiveOverflowBase = b.receiveOverflowTotal
		b.receiveOverflowRawLast = 0
	} else if observation.Packets < b.receiveOverflowRawLast {
		// The quic-go adapter extends the 32-bit kernel counter, but treat an
		// unexpected adapter reset as a new counter epoch instead of moving the
		// public cumulative value backwards.
		b.receiveOverflowBase = b.receiveOverflowTotal
	}
	before := b.receiveOverflowTotal
	b.receiveOverflowRawLast = observation.Packets
	b.receiveOverflowTotal = b.receiveOverflowBase + observation.Packets
	result.Packets = b.receiveOverflowTotal
	changed := b.receiveOverflowTotal > before
	b.receiveOverflowMu.Unlock()
	if changed {
		b.recordSessionEventAt(
			0, 0, telemetry.SessionEventReceiveOverflow, observation.Source, time.Now(),
			&telemetry.SessionEventMetrics{ReceiveQueueOverflow: before},
			&telemetry.SessionEventMetrics{ReceiveQueueOverflow: result.Packets},
		)
	}
	return result
}

// SessionTelemetry returns independent observations for the currently active
// QUIC sessions. Counters are scoped to a session and therefore reset when a
// configured endpoint reconnects with a new session generation.
func (b *Bind) SessionTelemetry() ([]telemetry.SessionObservation, uint64) {
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return nil, 0
	}
	state.mu.Lock()
	sessions := make([]*session, 0, len(state.sessions))
	for _, sess := range state.sessions {
		sessions = append(sessions, sess)
	}
	state.mu.Unlock()
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].role != sessions[j].role {
			return sessions[i].role == "outbound"
		}
		return sessions[i].id < sessions[j].id
	})
	omitted := uint64(0)
	if len(sessions) > maxSessionTelemetry {
		omitted = uint64(len(sessions) - maxSessionTelemetry)
		sessions = sessions[:maxSessionTelemetry]
	}
	sampledAt := time.Now()
	result := make([]telemetry.SessionObservation, 0, len(sessions))
	for _, sess := range sessions {
		result = append(result, sess.telemetry(sampledAt))
	}
	return result, omitted
}

// RecentSessionTelemetry returns immutable final observations retained after
// sessions leave the active set. The list is ordered by FinalSequence. Entries
// are evicted when either the fixed capacity or TTL is exceeded.
func (b *Bind) RecentSessionTelemetry() ([]telemetry.ClosedSessionObservation, uint64) {
	b.telemetryMu.Lock()
	b.pruneRecentSessionsLocked(time.Now())
	result := append([]telemetry.ClosedSessionObservation(nil), b.recentSessions...)
	evicted := b.recentEvicted
	b.telemetryMu.Unlock()
	return result, evicted
}

func (b *Bind) pruneRecentSessionsLocked(now time.Time) {
	first := 0
	for first < len(b.recentSessions) &&
		now.Sub(b.recentSessions[first].ClosedAt) >= recentSessionTelemetryTTL {
		first++
	}
	if first == 0 {
		return
	}
	b.recentEvicted += uint64(first)
	copy(b.recentSessions, b.recentSessions[first:])
	b.recentSessions = b.recentSessions[:len(b.recentSessions)-first]
}

func (b *Bind) retainClosedSession(observation telemetry.ClosedSessionObservation) {
	b.telemetryMu.Lock()
	b.pruneRecentSessionsLocked(observation.ClosedAt)
	b.recentSequence++
	observation.FinalSequence = b.recentSequence
	if replacement := b.replacements[observation.SessionID]; replacement != 0 {
		observation.ReplacedBySessionID = replacement
		delete(b.replacements, observation.SessionID)
	}
	b.recentSessions = append(b.recentSessions, observation)
	if excess := len(b.recentSessions) - maxRecentSessionTelemetry; excess > 0 {
		b.recentEvicted += uint64(excess)
		copy(b.recentSessions, b.recentSessions[excess:])
		b.recentSessions = b.recentSessions[:len(b.recentSessions)-excess]
	}
	b.telemetryMu.Unlock()
}

func (b *Bind) recordSessionReplacement(oldSessionID, newSessionID uint64) {
	if oldSessionID == 0 || newSessionID == 0 {
		return
	}
	b.telemetryMu.Lock()
	for index := range b.recentSessions {
		if b.recentSessions[index].SessionID == oldSessionID {
			b.recentSessions[index].ReplacedBySessionID = newSessionID
			b.telemetryMu.Unlock()
			return
		}
	}
	b.replacements[oldSessionID] = newSessionID
	b.telemetryMu.Unlock()
}

// SessionEvents returns a bounded cursor page. A stream ID mismatch is an
// explicit epoch boundary and must never be treated as an empty event range.
func (b *Bind) SessionEvents(
	eventStreamID string,
	afterSequence uint64,
	limit int,
) (telemetry.SessionEventBatch, error) {
	if limit <= 0 {
		limit = 256
	}
	if limit > maxSessionEventQuery {
		limit = maxSessionEventQuery
	}
	b.eventMu.Lock()
	defer b.eventMu.Unlock()
	if eventStreamID != "" && eventStreamID != b.eventStreamID {
		return telemetry.SessionEventBatch{}, fmt.Errorf(
			"event stream changed from %s to %s", eventStreamID, b.eventStreamID,
		)
	}
	batch := telemetry.SessionEventBatch{
		TelemetryVersion: telemetry.SessionEventTelemetryVersion,
		EventStreamID:    b.eventStreamID, EventsDroppedTotal: b.eventsDropped,
		LastSequence: min(afterSequence, b.eventSequence),
	}
	if len(b.events) == 0 {
		return batch, nil
	}
	batch.FirstAvailableSequence = b.events[0].EventSequence
	first := sort.Search(len(b.events), func(index int) bool {
		return b.events[index].EventSequence > afterSequence
	})
	last := min(len(b.events), first+limit)
	batch.Events = append([]telemetry.SessionEvent(nil), b.events[first:last]...)
	if len(batch.Events) != 0 {
		batch.LastSequence = batch.Events[len(batch.Events)-1].EventSequence
	}
	return batch, nil
}

func (b *Bind) recordSessionEventAt(
	sessionID, sessionGeneration uint64,
	eventType, reason string,
	wallTime time.Time,
	before, after *telemetry.SessionEventMetrics,
) {
	if wallTime.IsZero() {
		wallTime = time.Now()
	}
	b.eventMu.Lock()
	b.eventSequence++
	elapsed := wallTime.Sub(b.eventOrigin)
	if elapsed < 0 {
		elapsed = 0
	}
	event := telemetry.SessionEvent{
		TelemetryVersion: telemetry.SessionEventTelemetryVersion,
		EventStreamID:    b.eventStreamID, EventSequence: b.eventSequence,
		SessionID: sessionID, SessionGeneration: sessionGeneration,
		EventType: eventType, Reason: reason,
		MonotonicElapsedNS: elapsed.Nanoseconds(), WallTime: wallTime,
		Before: before, After: after,
	}
	b.events = append(b.events, event)
	if excess := len(b.events) - maxSessionEvents; excess > 0 {
		b.eventsDropped += uint64(excess)
		copy(b.events, b.events[excess:])
		b.events = b.events[:len(b.events)-excess]
	}
	b.eventMu.Unlock()
}

func sessionEventMetricsFromStats(
	stats telemetry.SessionStats,
	fecPolicy, endpoint string,
) *telemetry.SessionEventMetrics {
	return &telemetry.SessionEventMetrics{
		CongestionWindowBytes: stats.QUICCongestionWindowBytes,
		BytesInFlight:         stats.QUICBytesInFlight,
		BandwidthEstimateBps:  stats.QUICBandwidthEstimateBps,
		PacingRateBps:         stats.QUICPacingRateBps,
		SmoothedRTTUs:         stats.QUICSmoothedRTTUs,
		PathRTTUs:             stats.QUICPathRTTUs,
		QueueDelayUs:          stats.QUICQueueDelayUs,
		CongestionModelState:  stats.QUICCongestionModelState,
		PTOCount:              stats.QUICPTOCount,
		PacketsLost:           stats.QUICPacketsLost,
		SpuriousLossPackets:   stats.QUICSpuriousLossPackets,
		QueueDrops:            stats.QueueDrops,
		FECPolicy:             fecPolicy, Endpoint: endpoint,
	}
}

func transportEventMetrics(
	metrics *quiccarrier.ConnectionEventMetrics,
) *telemetry.SessionEventMetrics {
	if metrics == nil {
		return nil
	}
	return &telemetry.SessionEventMetrics{
		CongestionWindowBytes: metrics.CongestionWindowBytes,
		BytesInFlight:         metrics.BytesInFlight,
		BandwidthEstimateBps:  metrics.BandwidthEstimateBps,
		PacingRateBps:         metrics.PacingRateBps,
		SmoothedRTTUs:         metrics.SmoothedRTTUs,
		PathRTTUs:             metrics.PathRTTUs,
		QueueDelayUs:          metrics.QueueDelayUs,
		CongestionModelState:  metrics.CongestionModelState,
		PTOCount:              metrics.PTOCount,
		PacketsLost:           metrics.PacketsLost,
		SpuriousLossPackets:   metrics.SpuriousLossPackets,
	}
}

// AssociateSessionPeer records that WireGuard authenticated a packet from a
// peer on the named QUIC session. A session can carry more than one peer, so
// associations are accumulated rather than replaced.
func (b *Bind) AssociateSessionPeer(
	sessionID uint64,
	publicKey string,
	endpointGeneration uint64,
) bool {
	if sessionID == 0 || publicKey == "" {
		return false
	}
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	sess := state.sessions[sessionID]
	state.mu.Unlock()
	if sess == nil || sess.closed.Load() {
		return false
	}
	sess.mu.Lock()
	if previous := sess.authenticatedPeers[publicKey]; endpointGeneration < previous {
		endpointGeneration = previous
	}
	sess.authenticatedPeers[publicKey] = endpointGeneration
	sess.mu.Unlock()
	return true
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
		stats.QUICSpuriousLossPackets += current.SpuriousLossPackets
		stats.QUICPTOCount += current.PTOCount
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
		stats.QUICRTTVarUs = max(
			stats.QUICRTTVarUs,
			uint64(current.MeanDeviation/time.Microsecond),
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
		Attempts:            ep.reconnectAttempts,
		Failures:            ep.reconnectFailures,
		ConsecutiveFailures: ep.consecutiveFailures,
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
	policy := b.fecPolicies[ep.addr].policy
	state := b.state
	b.mu.Unlock()
	ep.fecPolicy = policy
	if state != nil {
		state.mu.Lock()
		if existing := state.endpoints[ep.addr]; existing != nil {
			ep = existing
			ep.mu.Lock()
			ep.configured = true
			ep.retired = false
			ep.fecPolicy = policy
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

func normalizeFECPolicy(policy string) (string, error) {
	if policy == "" {
		policy = "balanced"
	}
	switch policy {
	case "latency", "balanced", "throughput":
		return policy, nil
	default:
		return "", fmt.Errorf("unsupported peer FEC policy %q", policy)
	}
}

// AcquireEndpointFECPolicy associates an outbound endpoint with one peer FEC
// encoder policy. Different policies cannot share one configured endpoint,
// because WireGuard's outbound endpoint does not carry peer identity into the
// bind interface.
func (b *Bind) AcquireEndpointFECPolicy(
	endpoint netip.AddrPort,
	policy string,
) (func(), error) {
	endpoint, err := peerendpoint.Canonical(endpoint)
	if err != nil {
		return nil, fmt.Errorf("valid numeric endpoint is required: %w", err)
	}
	policy, err = normalizeFECPolicy(policy)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	lease := b.fecPolicies[endpoint]
	if lease.refs != 0 && lease.policy != policy {
		b.mu.Unlock()
		return nil, fmt.Errorf("endpoint %s is already associated with FEC policy %q", endpoint, lease.policy)
	}
	lease.policy = policy
	lease.refs++
	b.fecPolicies[endpoint] = lease
	state := b.state
	b.mu.Unlock()
	b.applyEndpointFECPolicy(state, endpoint, policy)
	return b.endpointFECPolicyRelease(endpoint, policy), nil
}

// ReplaceEndpointFECPolicy atomically changes the sole policy lease for an
// endpoint. The send worker flushes the old group before applying it.
func (b *Bind) ReplaceEndpointFECPolicy(
	endpoint netip.AddrPort,
	oldPolicy string,
	newPolicy string,
) (func(), error) {
	endpoint, err := peerendpoint.Canonical(endpoint)
	if err != nil {
		return nil, err
	}
	oldPolicy, err = normalizeFECPolicy(oldPolicy)
	if err != nil {
		return nil, err
	}
	newPolicy, err = normalizeFECPolicy(newPolicy)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	lease := b.fecPolicies[endpoint]
	if lease.refs != 1 || lease.policy != oldPolicy {
		b.mu.Unlock()
		return nil, fmt.Errorf("endpoint %s FEC policy lease changed concurrently", endpoint)
	}
	lease.policy = newPolicy
	b.fecPolicies[endpoint] = lease
	state := b.state
	b.mu.Unlock()
	b.applyEndpointFECPolicy(state, endpoint, newPolicy)
	return b.endpointFECPolicyRelease(endpoint, newPolicy), nil
}

func (b *Bind) endpointFECPolicyRelease(endpoint netip.AddrPort, policy string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			lease := b.fecPolicies[endpoint]
			if lease.refs == 0 || lease.policy != policy {
				b.mu.Unlock()
				return
			}
			lease.refs--
			if lease.refs == 0 {
				delete(b.fecPolicies, endpoint)
			} else {
				b.fecPolicies[endpoint] = lease
			}
			b.mu.Unlock()
		})
	}
}

func (b *Bind) applyEndpointFECPolicy(
	state *runState,
	endpoint netip.AddrPort,
	policy string,
) {
	if state == nil {
		return
	}
	state.mu.Lock()
	ep := state.endpoints[endpoint]
	if ep == nil {
		state.mu.Unlock()
		return
	}
	ep.mu.Lock()
	ep.fecPolicy = policy
	sess := ep.session
	ep.mu.Unlock()
	state.mu.Unlock()
	if sess != nil {
		sess.setFECPolicy(policy)
	}
}

// SetAuthenticatedSessionFECPolicy binds an accepted/roamed connection only
// after WireGuard identifies the peer that authenticated on that path.
func (b *Bind) SetAuthenticatedSessionFECPolicy(sessionID uint64, policy string) error {
	if sessionID == 0 {
		return nil
	}
	policy, err := normalizeFECPolicy(policy)
	if err != nil {
		return err
	}
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	sess := state.sessions[sessionID]
	state.mu.Unlock()
	if sess != nil {
		sess.setFECPolicy(policy)
	}
	return nil
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
		session.setCloseCause(
			telemetry.SessionCloseConfigurationRemoved,
			"configuration_removed",
			nil,
		)
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
		ep.mu.Lock()
		ep.pendingReplacement = session.id
		ep.mu.Unlock()
		session.setCloseCause(
			telemetry.SessionCloseEndpointReplaced,
			"endpoint_replaced",
			nil,
		)
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
		sess.stats.wgTxPackets.Add(1)
		sess.stats.wgTxBytes.Add(uint64(len(buf)))
		queue := sess.send
		copies := 1
		if priorityWireGuardDatagram(buf) {
			queue = sess.priority
			// Duplicate handshake/keepalive datagrams so a burst cannot wipe
			// out both copies at once; WireGuard dedupes the repeat by its
			// counter, so the duplicated send is protocol-safe.
			copies = 2
		}
		for i := 0; i < copies; i++ {
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
				sess.stats.queueDrops.Add(1)
				observation := sess.telemetry(time.Now())
				b.recordSessionEventAt(
					sess.id, sess.generation, telemetry.SessionEventQueueDrop,
					"send_queue_full", observation.SampledAt, nil,
					sessionEventMetricsFromStats(
						observation.Stats, sess.currentFECPolicy(), observation.CurrentEndpoint,
					),
				)
				b.debugf("send queue full: session=%d endpoint=%s", sess.id, sess.endpoint.addr)
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
	ep.sessionGeneration++
	replacesSessionID := ep.pendingReplacement
	ep.pendingReplacement = 0
	role := "inbound"
	configuredEndpoint := ""
	if ep.configured {
		role = "outbound"
		configuredEndpoint = ep.addr.String()
	}
	sess := &session{
		id: b.nextSession.Add(1), generation: ep.sessionGeneration,
		role: role, configuredEndpoint: configuredEndpoint,
		state: state, endpoint: ep, ctx: ctx, cancel: cancel,
		ready: make(chan struct{}), send: make(chan outboundPacket, state.cfg.QueueSize),
		priority: make(chan outboundPacket, max(64, state.cfg.QueueSize/8)),
		control:  make(chan []byte, 64), reconnectAttempt: reconnectAttempt,
		fecPolicyUpdates:   make(chan string, 1),
		remoteAddr:         ep.addr,
		authenticatedPeers: make(map[string]uint64),
		replacesSessionID:  replacesSessionID,
	}
	sess.fecDecoder = fec.NewDecoder()
	if state.cfg.FECMode == "auto" {
		profile := fecPolicyProfileFor(state.cfg, ep.fecPolicy)
		sess.fecPolicy = profile.name
		sess.fecFlushDeadline = profile.flushDeadline
		sess.fecEncoder = fec.NewEncoder(profile.dataShards, fec.NewController())
		// SetInterleave cannot fail at construction: no groups are in flight.
		_ = sess.fecEncoder.SetInterleave(profile.interleave)
	}
	state.sessions[sess.id] = sess
	b.recordSessionReplacement(replacesSessionID, sess.id)
	b.stats.activeSessions.Add(1)
	if role == "outbound" {
		b.recordSessionEventAt(
			sess.id, sess.generation, telemetry.SessionEventCreated, role,
			time.Now(), nil, nil,
		)
	}
	return sess
}

func (b *Bind) dialSession(sess *session) {
	defer sess.state.wg.Done()
	b.debugf("dialing QUIC session: session=%d remote=%s", sess.id, sess.endpoint.addr)
	ctx, cancel := context.WithTimeout(sess.ctx, sess.state.cfg.HandshakeTimeout)
	defer cancel()
	qconn, err := sess.state.carrier.Dial(ctx, sess.endpoint.addr)
	if err != nil {
		reason, class, message := quiccarrier.ClassifyConnectionError(err)
		sess.setCloseCause(reason, class, errors.New(message))
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
	generation          uint64
	role                string
	configuredEndpoint  string
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
	authenticatedPeers  map[string]uint64
	readyOnce           sync.Once
	closeOnce           sync.Once
	closed              atomic.Bool
	reconnectAttempt    bool
	replacesSessionID   uint64
	closeReason         string
	closeErrorClass     string
	closeError          string
	fecEncoder          *fec.Encoder
	fecDecoder          *fec.Decoder
	fecPathSampleFrames uint32
	fecPolicy           string
	fecPolicyUpdates    chan string
	fecFlushDeadline    time.Duration
	stats               sessionCounters
}

func (s *session) telemetry(sampledAt time.Time) telemetry.SessionObservation {
	s.mu.Lock()
	conn := s.conn
	remoteAddr := s.remoteAddr
	establishedAt := s.establishedAt
	peers := make([]telemetry.SessionPeerObservation, 0, len(s.authenticatedPeers))
	for publicKey, generation := range s.authenticatedPeers {
		peers = append(peers, telemetry.SessionPeerObservation{
			PublicKey: publicKey, EndpointGeneration: generation, Authenticated: true,
		})
	}
	s.mu.Unlock()
	sort.Slice(peers, func(i, j int) bool { return peers[i].PublicKey < peers[j].PublicKey })

	state := "connecting"
	var establishedAtValue *time.Time
	if conn != nil {
		state = "established"
		copy := establishedAt
		establishedAtValue = &copy
	}
	currentEndpoint := ""
	if remoteAddr.IsValid() {
		currentEndpoint = remoteAddr.String()
	}
	stats := telemetry.SessionStats{
		WGTxPackets: s.stats.wgTxPackets.Load(), WGTxBytes: s.stats.wgTxBytes.Load(),
		WGRxPackets: s.stats.wgRxPackets.Load(), WGRxBytes: s.stats.wgRxBytes.Load(),
		WireTxPackets: s.stats.wireTxPackets.Load(), WireTxBytes: s.stats.wireTxBytes.Load(),
		WireRxPackets: s.stats.wireRxPackets.Load(), WireRxBytes: s.stats.wireRxBytes.Load(),
		QueueDrops: s.stats.queueDrops.Load(), FECDataTx: s.stats.fecDataTx.Load(),
		FECParityTx: s.stats.fecParityTx.Load(), FECRawLost: s.stats.fecRawLost.Load(),
		FECRecovered: s.stats.fecRecovered.Load(), FECUnrecovered: s.stats.fecUnrecovered.Load(),
		SendQueueDepth: uint64(len(s.send)), PriorityQueueDepth: uint64(len(s.priority)),
		ControlQueueDepth: uint64(len(s.control)),
	}
	if s.fecEncoder != nil {
		parity, lossEstimatePPM := s.fecEncoder.Stats()
		stats.FECCurrentParityShards = uint64(parity)
		stats.FECLossEstimatePPM = lossEstimatePPM
	}
	if conn != nil {
		current := conn.Stats()
		stats.QUICBytesSent = current.BytesSent
		stats.QUICPacketsSent = current.PacketsSent
		stats.QUICBytesReceived = current.BytesReceived
		stats.QUICPacketsReceived = current.PacketsReceived
		stats.QUICBytesAcked = current.BytesAcked
		stats.QUICPacketsAcked = current.PacketsAcked
		stats.QUICBytesLost = current.BytesLost
		stats.QUICPacketsLost = current.PacketsLost
		stats.QUICSpuriousLossPackets = current.SpuriousLossPackets
		stats.QUICPTOCount = current.PTOCount
		stats.QUICMinRTTUs = uint64(current.MinRTT / time.Microsecond)
		stats.QUICLatestRTTUs = uint64(current.LatestRTT / time.Microsecond)
		stats.QUICSmoothedRTTUs = uint64(current.SmoothedRTT / time.Microsecond)
		stats.QUICRTTVarUs = uint64(current.MeanDeviation / time.Microsecond)
		stats.QUICCongestionWindowBytes = current.CongestionWindow
		stats.QUICBytesInFlight = current.BytesInFlight
		stats.QUICBandwidthEstimateBps = current.BandwidthEstimate
		stats.QUICPacingRateBps = current.PacingRate
		stats.QUICPathRTTUs = uint64(current.PropagationRTT / time.Microsecond)
		stats.QUICQueueDelayUs = uint64(current.QueueDelay / time.Microsecond)
		stats.QUICFECRecoverableLossPPM = current.FECRecoverableLossPPM
		stats.QUICFECResidualLossPPM = current.FECResidualLossPPM
		stats.QUICCongestionModelState = current.CongestionModelState
		stats.QUICDatagramSendQueueLen = current.DatagramSendQueueLen
	}
	return telemetry.SessionObservation{
		TelemetryVersion: telemetry.SessionTelemetryVersion,
		SessionID:        s.id, SessionGeneration: s.generation,
		Role: s.role, State: state, ConfiguredEndpoint: s.configuredEndpoint,
		CurrentEndpoint: currentEndpoint, EstablishedAt: establishedAtValue,
		SampledAt: sampledAt, Peers: peers, ReplacesSessionID: s.replacesSessionID,
		Stats: stats,
	}
}

func (s *session) setCloseCause(reason, class string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeReason != "" && s.closeReason != telemetry.SessionCloseUnknown {
		return
	}
	if reason == "" {
		reason = telemetry.SessionCloseUnknown
	}
	s.closeReason = reason
	s.closeErrorClass = class
	if err == nil {
		return
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 512 {
		message = message[:512]
	}
	s.closeError = message
}

func (s *session) finalTelemetry(closedAt time.Time) telemetry.ClosedSessionObservation {
	active := s.telemetry(closedAt)
	s.mu.Lock()
	reason, class, lastError := s.closeReason, s.closeErrorClass, s.closeError
	s.mu.Unlock()
	if reason == "" {
		reason = telemetry.SessionCloseUnknown
	}
	return telemetry.ClosedSessionObservation{
		TelemetryVersion: telemetry.RecentSessionTelemetryVersion,
		SessionID:        active.SessionID, SessionGeneration: active.SessionGeneration,
		Role: active.Role, State: "closed",
		ConfiguredEndpoint: active.ConfiguredEndpoint,
		CurrentEndpoint:    active.CurrentEndpoint,
		EstablishedAt:      active.EstablishedAt, ClosedAt: closedAt, SampledAt: closedAt,
		Peers: active.Peers, CloseReason: reason, ErrorClass: class,
		LastError: lastError, ReplacesSessionID: active.ReplacesSessionID,
		Final: true, FinalStats: active.Stats,
	}
}

type fecPolicyProfile struct {
	name          string
	dataShards    int
	interleave    int
	flushDeadline time.Duration
}

func fecPolicyProfileFor(cfg Config, policy string) fecPolicyProfile {
	policy, err := normalizeFECPolicy(policy)
	if err != nil {
		policy = "balanced"
	}
	profile := fecPolicyProfile{
		name: policy, dataShards: cfg.FECDataShards,
		interleave: cfg.FECInterleave, flushDeadline: cfg.FECFlushDeadline,
	}
	switch policy {
	case "latency":
		profile.dataShards = min(profile.dataShards, 4)
		profile.interleave = 1
		profile.flushDeadline = min(profile.flushDeadline, time.Millisecond)
	case "throughput":
		profile.interleave = max(profile.interleave, 2)
		profile.flushDeadline = max(profile.flushDeadline, 4*time.Millisecond)
	}
	if profile.flushDeadline <= 0 {
		profile.flushDeadline = time.Millisecond
	}
	return profile
}

func (s *session) setFECPolicy(policy string) {
	if s.fecPolicyUpdates == nil || s.closed.Load() {
		return
	}
	select {
	case s.fecPolicyUpdates <- policy:
		return
	default:
	}
	// Only the newest requested profile matters. The send worker is the sole
	// encoder owner and will still flush whichever profile is active there.
	select {
	case <-s.fecPolicyUpdates:
	default:
	}
	select {
	case s.fecPolicyUpdates <- policy:
	default:
	}
}

func (s *session) setConn(qconn *quiccarrier.Connection) {
	qconn.SetEventObserver(s.recordTransportEvent)
	if s.role == "inbound" {
		s.endpoint.owner.recordSessionEventAt(
			s.id, s.generation, telemetry.SessionEventCreated, s.role,
			time.Now(), nil, nil,
		)
	}
	s.mu.Lock()
	s.conn = qconn
	s.establishedAt = time.Now()
	s.mu.Unlock()
	s.endpoint.mu.Lock()
	if s.endpoint.configured {
		s.endpoint.nextReconnect = time.Time{}
	}
	s.endpoint.mu.Unlock()
	observation := s.telemetry(time.Now())
	s.endpoint.owner.recordSessionEventAt(
		s.id, s.generation, telemetry.SessionEventEstablished, s.role,
		observation.SampledAt, nil,
		sessionEventMetricsFromStats(
			observation.Stats, s.currentFECPolicy(), observation.CurrentEndpoint,
		),
	)
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *session) recordTransportEvent(event quiccarrier.ConnectionEvent) {
	eventType := event.Type
	switch event.Type {
	case "controller_state":
		eventType = telemetry.SessionEventControllerState
	case "cwnd_reduction":
		eventType = telemetry.SessionEventCwndReduction
	case "pto":
		eventType = telemetry.SessionEventPTO
	case "loss":
		eventType = telemetry.SessionEventLoss
	case "spurious_loss":
		eventType = telemetry.SessionEventSpuriousLoss
	case "path_rtt":
		eventType = telemetry.SessionEventPathRTT
	}
	s.endpoint.owner.recordSessionEventAt(
		s.id, s.generation, eventType, event.Reason, event.WallTime,
		transportEventMetrics(event.Before), transportEventMetrics(event.After),
	)
}

func (s *session) currentFECPolicy() string {
	s.mu.Lock()
	policy := s.fecPolicy
	s.mu.Unlock()
	return policy
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.cancel()
		s.state.mu.Lock()
		stateClosing := s.state.closing
		s.state.mu.Unlock()
		if stateClosing {
			s.setCloseCause(telemetry.SessionCloseLocalShutdown, "local_shutdown", nil)
		}
		closedAt := time.Now()
		final := s.finalTelemetry(closedAt)
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
			if configured && final.CloseReason != telemetry.SessionCloseLocalShutdown &&
				final.CloseReason != telemetry.SessionCloseConfigurationRemoved {
				s.endpoint.pendingReplacement = s.id
			}
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
		s.endpoint.owner.retainClosedSession(final)
		s.endpoint.owner.recordSessionEventAt(
			s.id, s.generation, telemetry.SessionEventClosed, final.CloseReason,
			closedAt, nil,
			sessionEventMetricsFromStats(
				final.FinalStats, s.currentFECPolicy(), final.CurrentEndpoint,
			),
		)
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
		s.stats.wireTxPackets.Add(1)
		s.stats.wireTxBytes.Add(uint64(packetBytes))
		if fecPacket {
			switch kind {
			case fec.KindData:
				s.endpoint.owner.stats.fecDataTx.Add(1)
				s.stats.fecDataTx.Add(1)
			case fec.KindParity:
				s.endpoint.owner.stats.fecParityTx.Add(1)
				s.stats.fecParityTx.Add(1)
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
	// flushPending emits any in-flight FEC groups so a priority datagram and
	// its duplicate land in separate groups and survive the same burst.
	flushPending := func() bool {
		if s.fecEncoder == nil {
			return true
		}
		packets, err := s.fecEncoder.Flush()
		return err == nil && sendPackets(packets)
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
		timer.Reset(s.fecFlushDeadline)
		timerActive = true
	}
	applyFECPolicy := func(policy string) bool {
		if s.fecEncoder == nil {
			return true
		}
		profile := fecPolicyProfileFor(s.state.cfg, policy)
		previousPolicy := s.currentFECPolicy()
		if profile.name == previousPolicy {
			return true
		}
		stopTimer()
		packets, err := s.fecEncoder.Reconfigure(profile.dataShards, profile.interleave)
		if err != nil || !sendPackets(packets) {
			return false
		}
		s.mu.Lock()
		s.fecPolicy = profile.name
		s.fecFlushDeadline = profile.flushDeadline
		s.mu.Unlock()
		s.endpoint.owner.recordSessionEventAt(
			s.id, s.generation, telemetry.SessionEventFECPolicy, "runtime_update",
			time.Now(),
			&telemetry.SessionEventMetrics{FECPolicy: previousPolicy},
			&telemetry.SessionEventMetrics{FECPolicy: profile.name},
		)
		return true
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
		case policy := <-s.fecPolicyUpdates:
			if !applyFECPolicy(policy) {
				return
			}
			continue
		default:
		}
		select {
		case control := <-s.control:
			if !sendPacket(control) {
				return
			}
		case policy := <-s.fecPolicyUpdates:
			if !applyFECPolicy(policy) {
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
			if !flushPending() {
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
			if !flushPending() {
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
		previousRemote := s.remoteAddr
		s.remoteAddr = datagram.RemoteAddr
		s.mu.Unlock()
		if previousRemote.IsValid() && previousRemote != datagram.RemoteAddr {
			s.endpoint.owner.recordSessionEventAt(
				s.id, s.generation, telemetry.SessionEventEndpointMoved,
				"authenticated_path_observed", time.Now(),
				&telemetry.SessionEventMetrics{Endpoint: previousRemote.String()},
				&telemetry.SessionEventMetrics{Endpoint: datagram.RemoteAddr.String()},
			)
		}
		receiveEndpoint := s.endpointForAddrPort(datagram.RemoteAddr)
		s.endpoint.owner.stats.wireRxPackets.Add(1)
		s.endpoint.owner.stats.wireRxBytes.Add(uint64(len(wirePacket)))
		s.stats.wireRxPackets.Add(1)
		s.stats.wireRxBytes.Add(uint64(len(wirePacket)))
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
					reason, class, message := quiccarrier.ClassifyConnectionError(received.err)
					s.setCloseCause(reason, class, errors.New(message))
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
	s.endpoint.mu.Lock()
	fallback := s.endpoint.fallback
	if s.endpoint.configured {
		fallback = s.endpoint
	}
	s.endpoint.mu.Unlock()
	return &Endpoint{
		owner: s.endpoint.owner, addr: remote,
		receiveSequence: s.endpoint.owner.receiveSequence.Add(1),
		session:         s, fallback: fallback,
	}
}

func (s *session) sendFECFeedback(feedbacks []fec.Feedback) {
	for _, feedback := range feedbacks {
		s.endpoint.owner.stats.fecRawLost.Add(uint64(feedback.Missing))
		s.endpoint.owner.stats.fecRecovered.Add(uint64(feedback.Recovered))
		s.stats.fecRawLost.Add(uint64(feedback.Missing))
		s.stats.fecRecovered.Add(uint64(feedback.Recovered))
		if feedback.Missing > feedback.Recovered {
			s.endpoint.owner.stats.fecUnrecovered.Add(uint64(feedback.Missing - feedback.Recovered))
			s.stats.fecUnrecovered.Add(uint64(feedback.Missing - feedback.Recovered))
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
	s.stats.wgRxPackets.Add(1)
	s.stats.wgRxBytes.Add(uint64(len(packet)))
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
		s.stats.queueDrops.Add(1)
		observation := s.telemetry(time.Now())
		s.endpoint.owner.recordSessionEventAt(
			s.id, s.generation, telemetry.SessionEventQueueDrop,
			"receive_queue_full", observation.SampledAt, nil,
			sessionEventMetricsFromStats(
				observation.Stats, s.currentFECPolicy(), observation.CurrentEndpoint,
			),
		)
		if rp.release != nil {
			rp.release(rp.data)
		}
	}
}
