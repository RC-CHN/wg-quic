package armorbind

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

func TestSessionReplacementDoesNotRetainEvictedHistory(t *testing.T) {
	b := New(DefaultConfig())
	for id := uint64(1); id <= 1000; id++ {
		b.retainClosedSession(telemetry.ClosedSessionObservation{SessionID: id, ClosedAt: time.Now()})
		if id > maxRecentSessionTelemetry {
			// A configured peer may reconnect after its old closed record was
			// evicted by other peers. That record will never be finalized again.
			b.recordSessionReplacement(id-maxRecentSessionTelemetry, id+1000, false)
		}
	}
	if len(b.replacements) != 0 {
		t.Fatalf("retained %d replacement links for already-finalized sessions", len(b.replacements))
	}
}

func TestSessionReplacementDoesNotRetainExpiredHistory(t *testing.T) {
	b := New(DefaultConfig())
	b.retainClosedSession(telemetry.ClosedSessionObservation{SessionID: 1, ClosedAt: time.Now().Add(-recentSessionTelemetryTTL)})
	b.RecentSessionTelemetry()
	b.recordSessionReplacement(1, 2, false)
	if len(b.replacements) != 0 {
		t.Fatal("retained a replacement for a closed session that expired")
	}
}

func TestSessionReplacementRacesWithClose(t *testing.T) {
	b := New(DefaultConfig())
	state := &runState{ctx: context.Background(), cfg: DefaultConfig(), sessions: make(map[uint64]*session)}
	ep := &Endpoint{owner: b, addr: netip.MustParseAddrPort("127.0.0.1:51820")}
	ep.route.Store(&endpointRoute{configured: true})
	for range 100 {
		state.mu.Lock()
		ep.mu.Lock()
		old := b.newSessionLocked(state, ep, false)
		ep.pendingReplacement = old.id
		ep.mu.Unlock()
		state.mu.Unlock()
		closed := make(chan struct{})
		go func() { old.close(); close(closed) }()
		state.mu.Lock()
		ep.mu.Lock()
		next := b.newSessionLocked(state, ep, false)
		ep.mu.Unlock()
		state.mu.Unlock()
		<-closed
		recent, _ := b.RecentSessionTelemetry()
		found := false
		for _, observation := range recent {
			if observation.SessionID == old.id {
				found = true
				if observation.ReplacedBySessionID != next.id {
					t.Fatalf("session %d replacement = %d, want %d", old.id, observation.ReplacedBySessionID, next.id)
				}
			}
		}
		if !found || len(b.replacements) != 0 {
			t.Fatalf("final observation found=%v, dangling links=%d", found, len(b.replacements))
		}
		next.close()
	}
}
