package armorbind

import (
	"errors"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

// state.mu protects both admission and registration. Inbound caps leave room
// for configured outbound peers even when strangers fill the inbound budget.
func (s *runState) admitSessionLocked(inbound bool) bool {
	return len(s.sessions) < s.cfg.MaxSessions && (!inbound ||
		(s.inboundSessions < s.cfg.MaxInboundSessions && s.pendingInbound < s.cfg.MaxPendingInboundSessions))
}

func (s *session) expireAuthentication() {
	s.state.mu.Lock()
	expired := !s.closed.Load() && s.authentication.CompareAndSwap(0, 2)
	if expired {
		s.state.pendingInbound--
	}
	s.state.mu.Unlock()
	if !expired {
		return
	}
	s.endpoint.owner.stats.authenticationTimeouts.Add(1)
	s.setCloseCause(telemetry.SessionCloseAuthenticationTimeout, "authentication_timeout", errors.New("WireGuard authentication deadline exceeded"))
	s.close()
}

// Unauthenticated traffic cannot occupy the whole shared WireGuard queue.
// Authenticated traffic retains the existing allocation-free enqueue path.
func (s *session) reserveUnauthenticatedReceive() bool {
	limit := int32(min(32, max(1, s.state.cfg.QueueSize/4)))
	if s.pendingReceive.Add(1) <= limit {
		return true
	}
	s.pendingReceive.Add(-1)
	return false
}

func (p receivedPacket) dispose() {
	if p.reservation != nil {
		p.reservation.pendingReceive.Add(-1)
	}
	if p.release != nil {
		p.release(p.data)
	}
}
