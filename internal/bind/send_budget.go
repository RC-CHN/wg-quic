package armorbind

import (
	"time"

	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

// The bind and QUIC queues budget 20 ms and 5 ms of service respectively.
// Packet floors tolerate scheduling jitter, while packet age also catches a
// stale bandwidth estimate immediately after a capacity drop. Expiration is
// applied to complete WireGuard datagrams, before fragmentation / FEC coding.
func (s *session) updateSendBudget(bandwidth, pacing uint64, startup bool) {
	rate := bandwidth
	if rate == 0 || (pacing != 0 && pacing < rate) {
		rate = pacing
	}
	if rate == 0 {
		return
	}
	budget := max(uint64(6000), rate/400)
	// During model discovery, a small rate estimate must not prevent TCP
	// from offering enough data to discover a faster path. Packet age still
	// bounds this startup allowance on genuinely slow links.
	if startup {
		budget = max(budget, 64*1024)
	}
	s.sendBudgetBytes.Store(int64(min(uint64(cap(s.send))*1500, budget)))
	age := max(100*time.Millisecond, time.Duration(min(uint64(2*time.Second), uint64(6000*8)*uint64(time.Second)/rate)))
	s.sendAgeNanos.Store(int64(age))
}

func (s *session) sendMaxAge() time.Duration {
	if age := s.sendAgeNanos.Load(); age > 0 {
		return time.Duration(age)
	}
	return 100 * time.Millisecond
}

func (s *session) reserveSendBytes(n int) bool {
	limit := s.sendBudgetBytes.Load()
	if limit == 0 {
		limit = 64 * 1024 // bounded startup headroom before a delivery sample
	}
	for {
		current := s.sendBytes.Load()
		// Always permit one complete datagram, including a large offload
		// packet; the packet count and frame-size checks still bound memory.
		if current != 0 && current+int64(n) > limit {
			return false
		}
		if s.sendBytes.CompareAndSwap(current, current+int64(n)) {
			return true
		}
	}
}

func (s *session) releaseSendBytes(packet outboundPacket) {
	if !packet.queuedAt.IsZero() {
		s.sendBytes.Add(-int64(len(packet.preparedFrame)))
	}
}

func (s *session) recordSendDrop(reason string) {
	b := s.endpoint.owner
	if reason == "send_queue_expired" {
		b.stats.sendQueueExpired.Add(1)
		s.stats.sendQueueExpired.Add(1)
	}
	b.stats.queueDrops.Add(1)
	s.stats.queueDrops.Add(1)
	if s.claimQueueDropEvent(time.Now()) {
		observation := s.telemetry(time.Now())
		b.recordSessionEventAt(s.id, s.generation, telemetry.SessionEventQueueDrop,
			reason, observation.SampledAt, nil,
			sessionEventMetricsFromStats(observation.Stats, s.currentFECPolicy(), observation.CurrentEndpoint))
	}
}
