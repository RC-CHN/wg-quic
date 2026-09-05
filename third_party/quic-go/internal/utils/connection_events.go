package utils

import (
	"sync"
	"time"
)

const maxConnectionEvents = 256

// ConnectionEventMetrics is a typed snapshot captured around one low-volume
// transport/controller event. Units are part of the field names.
type ConnectionEventMetrics struct {
	CongestionWindowBytes uint64
	BytesInFlight         uint64
	BandwidthEstimateBps  uint64
	PacingRateBps         uint64
	SmoothedRTTUs         uint64
	PathRTTUs             uint64
	QueueDelayUs          uint64
	CongestionModelState  uint64
	PTOCount              uint64
	PacketsLost           uint64
	SpuriousLossPackets   uint64
}

// ConnectionEvent is a connection-local event. Sequence is only used to
// preserve order while a newly attached observer drains handshake history.
type ConnectionEvent struct {
	Sequence           uint64
	Type               string
	Reason             string
	WallTime           time.Time
	MonotonicElapsedNS int64
	Before             *ConnectionEventMetrics
	After              *ConnectionEventMetrics
}

type connectionEventJournal struct {
	mu          sync.Mutex
	origin      time.Time
	sequence    uint64
	events      []ConnectionEvent
	observer    func(ConnectionEvent)
	lastMetrics ConnectionEventMetrics
	hasMetrics  bool
}

func (s *ConnectionStats) EventMetrics() ConnectionEventMetrics {
	return ConnectionEventMetrics{
		CongestionWindowBytes: s.CongestionWindow.Load(),
		BytesInFlight:         s.BytesInFlight.Load(),
		BandwidthEstimateBps:  s.BandwidthEstimate.Load(),
		PacingRateBps:         s.PacingRate.Load(),
		SmoothedRTTUs:         nonNegativeMicros(s.SmoothedRTT.Load()),
		PathRTTUs:             nonNegativeMicros(s.PropagationRTT.Load()),
		QueueDelayUs:          nonNegativeMicros(s.QueueDelay.Load()),
		CongestionModelState:  s.CongestionModelState.Load(),
		PTOCount:              s.PTOCount.Load(),
		PacketsLost:           s.PacketsLost.Load(),
		SpuriousLossPackets:   s.SpuriousLossPackets.Load(),
	}
}

func nonNegativeMicros(nanoseconds int64) uint64 {
	if nanoseconds <= 0 {
		return 0
	}
	return uint64(time.Duration(nanoseconds) / time.Microsecond)
}

func (s *ConnectionStats) RecordControllerSnapshot(reason string) {
	after := s.EventMetrics()
	j := &s.events
	j.mu.Lock()
	if !j.hasMetrics {
		j.lastMetrics = after
		j.hasMetrics = true
		j.mu.Unlock()
		return
	}
	before := j.lastMetrics
	j.lastMetrics = after
	if after.PacketsLost > before.PacketsLost ||
		after.CongestionWindowBytes < before.CongestionWindowBytes ||
		after.CongestionModelState != before.CongestionModelState ||
		after.PathRTTUs != before.PathRTTUs {
		j.recordControllerTransitionsLocked(reason, before, after)
	}
	j.mu.Unlock()
}

// Keep snapshot addresses out of the common no-event path. Only retained
// transitions need heap-backed metrics; passing values here keeps ordinary
// per-packet observations on the stack while preserving immutable history.
func (j *connectionEventJournal) recordControllerTransitionsLocked(reason string, before, after ConnectionEventMetrics) {
	if after.PacketsLost > before.PacketsLost {
		j.recordLocked("loss", reason, &before, &after)
	}
	if after.CongestionWindowBytes < before.CongestionWindowBytes {
		j.recordLocked("cwnd_reduction", reason, &before, &after)
	}
	if after.CongestionModelState != before.CongestionModelState {
		j.recordLocked("controller_state", reason, &before, &after)
	}
	if after.PathRTTUs != before.PathRTTUs {
		j.recordLocked("path_rtt", reason, &before, &after)
	}
}

func (s *ConnectionStats) RecordEvent(eventType, reason string) {
	after := s.EventMetrics()
	j := &s.events
	j.mu.Lock()
	var before *ConnectionEventMetrics
	if j.hasMetrics {
		copy := j.lastMetrics
		before = &copy
	}
	j.lastMetrics = after
	j.hasMetrics = true
	j.recordLocked(eventType, reason, before, &after)
	j.mu.Unlock()
}

func (j *connectionEventJournal) recordLocked(
	eventType, reason string,
	before, after *ConnectionEventMetrics,
) {
	now := time.Now()
	if j.origin.IsZero() {
		j.origin = now
	}
	j.sequence++
	event := ConnectionEvent{
		Sequence: j.sequence, Type: eventType, Reason: reason,
		WallTime: now, MonotonicElapsedNS: now.Sub(j.origin).Nanoseconds(),
		Before: before, After: after,
	}
	j.events = append(j.events, event)
	if len(j.events) > maxConnectionEvents {
		copy(j.events, j.events[len(j.events)-maxConnectionEvents:])
		j.events = j.events[:maxConnectionEvents]
	}
	if j.observer != nil {
		j.observer(event)
	}
}

// SetEventObserver installs the sole event observer and synchronously drains
// retained handshake events before allowing newer callbacks through.
func (s *ConnectionStats) SetEventObserver(observer func(ConnectionEvent)) {
	j := &s.events
	j.mu.Lock()
	j.observer = observer
	if observer != nil {
		for _, event := range j.events {
			observer(event)
		}
	}
	j.mu.Unlock()
}
