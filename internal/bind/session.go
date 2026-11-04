package armorbind

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RC-CHN/wg-quic/internal/telemetry"
	"github.com/RC-CHN/wg-quic/internal/transport/fec"
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

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
		ep.route.Store(&endpointRoute{fallback: state.endpoints[remote]})
		if !state.admitSessionLocked(true) {
			state.mu.Unlock()
			b.stats.sessionAdmissionRejected.Add(1)
			qconn.CloseWithError("session admission limit")
			continue
		}
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
	if !ep.isConfigured() {
		fallback := ep.currentRoute().fallback
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
	if !state.admitSessionLocked(false) {
		ep.mu.Unlock()
		state.mu.Unlock()
		b.stats.sessionAdmissionRejected.Add(1)
		return nil, errSessionAdmissionLimit
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
	if ep.isConfigured() {
		role = "outbound"
		configuredEndpoint = ep.addr.String()
	}
	if role == "inbound" {
		state.inboundSessions++
		state.pendingInbound++
	}
	sess := &session{
		id: b.nextSession.Add(1), generation: ep.sessionGeneration,
		role: role, configuredEndpoint: configuredEndpoint,
		state: state, endpoint: ep, ctx: ctx, cancel: cancel,
		ready: make(chan struct{}), send: make(chan outboundPacket, state.cfg.QueueSize),
		sendSpace: make(chan struct{}, 1),
		priority:  make(chan outboundPacket, max(64, state.cfg.QueueSize/8)),
		control:   make(chan []byte, 64), reconnectAttempt: reconnectAttempt,
		fecPolicyUpdates:   make(chan string, 1),
		authenticatedPeers: make(map[string]uint64),
		replacesSessionID:  replacesSessionID,

		queueDropEventInterval: state.cfg.QueueDropEventInterval,
	}
	initialRemote := ep.addr
	sess.remoteAddr.Store(&initialRemote)
	if route := ep.currentRoute(); route.configured {
		sess.receiveRoute = &endpointRoute{fallback: ep}
	} else {
		sess.receiveRoute = ep.route.Load()
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
	b.recordSessionReplacement(replacesSessionID, sess.id, state.sessions[replacesSessionID] != nil)
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
		if sess.endpoint.isConfigured() && sess.endpoint.session == sess {
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
	if sess.role == "inbound" {
		timer := time.AfterFunc(sess.state.cfg.InboundAuthenticationTimeout, sess.expireAuthentication)
		defer timer.Stop()
	}
	priorityDone := make(chan struct{})
	go func() {
		defer close(priorityDone)
		defer sess.cancel()
		sess.prioritySendLoop()
	}()
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		// A local send failure need not close the QUIC receive half. Retire
		// both workers so a live transport cannot hide a dead send queue.
		defer sess.cancel()
		sess.sendLoop()
	}()
	sess.receiveLoop()
	sess.cancel()
	<-sendDone
	<-priorityDone
}

type session struct {
	id                 uint64
	generation         uint64
	role               string
	configuredEndpoint string
	state              *runState
	endpoint           *Endpoint
	ctx                context.Context
	cancel             context.CancelFunc
	ready              chan struct{}
	send               chan outboundPacket
	sendSpace          chan struct{} // coalesced notification of released data capacity
	sendBytes          atomic.Int64
	sendBudgetBytes    atomic.Int64
	sendAgeNanos       atomic.Int64
	sendDelayMaxUs     atomic.Uint64
	priority           chan outboundPacket
	control            chan []byte
	mu                 sync.Mutex
	conn               *quiccarrier.Connection
	remoteAddr         atomic.Pointer[netip.AddrPort]
	// receiveRoute is the routing snapshot stamped onto every per-datagram
	// Endpoint snapshot. It is computed once at construction because the
	// session's endpoint identity never changes afterwards; the pointer is
	// shared with the endpoint's own immutable snapshot when possible.
	receiveRoute       *endpointRoute
	establishedAt      time.Time
	authenticatedPeers map[string]uint64
	readyOnce          sync.Once
	closeOnce          sync.Once
	closed             atomic.Bool
	// 0 pending, 1 authenticated, 2 expired/closed; transitions serialize admission accounting.
	authentication      atomic.Uint32
	pendingReceive      atomic.Int32
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
	// queueDropEventInterval bounds queue-drop telemetry snapshots and
	// session events; drops inside one window only bump the queueDrops
	// counters. lastQueueDropEventNS records the claimed window start.
	queueDropEventInterval time.Duration
	lastQueueDropEventNS   atomic.Int64
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
	if s.endpoint.isConfigured() {
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
		// Publish before removal under the same lock used by replacement
		// registration. There must be no gap where the old session is absent
		// from both the active set and the final-observation history.
		s.endpoint.owner.retainClosedSession(final)
		delete(s.state.sessions, s.id)
		if s.role == "inbound" {
			s.state.inboundSessions--
			if s.authentication.Swap(2) == 0 {
				s.state.pendingInbound--
			}
		}
		s.state.mu.Unlock()
		s.endpoint.owner.stats.activeSessions.Add(^uint64(0))
		s.endpoint.mu.Lock()
		configured := s.endpoint.isConfigured()
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
