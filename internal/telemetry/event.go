package telemetry

import "time"

const SessionEventTelemetryVersion = 1

const (
	SessionEventCreated         = "session_created"
	SessionEventEstablished     = "session_established"
	SessionEventClosed          = "session_closed"
	SessionEventEndpointMoved   = "endpoint_migrated"
	SessionEventControllerState = "controller_state"
	SessionEventCwndReduction   = "cwnd_reduction"
	SessionEventPTO             = "pto"
	SessionEventLoss            = "loss"
	SessionEventSpuriousLoss    = "spurious_loss"
	SessionEventPathRTT         = "path_rtt"
	SessionEventFECPolicy       = "fec_policy"
	SessionEventQueueDrop       = "queue_drop"
	SessionEventReceiveOverflow = "receive_queue_overflow"
)

// SessionEventMetrics is a typed event snapshot. Zero is a valid value, so
// Before and After pointers communicate whether a side is available.
type SessionEventMetrics struct {
	CongestionWindowBytes uint64 `json:"congestion_window_bytes"`
	BytesInFlight         uint64 `json:"bytes_in_flight"`
	BandwidthEstimateBps  uint64 `json:"bandwidth_estimate_bps"`
	PacingRateBps         uint64 `json:"pacing_rate_bps"`
	SmoothedRTTUs         uint64 `json:"smoothed_rtt_us"`
	PathRTTUs             uint64 `json:"path_rtt_us"`
	QueueDelayUs          uint64 `json:"queue_delay_us"`
	CongestionModelState  uint64 `json:"congestion_model_state"`
	PTOCount              uint64 `json:"pto_count"`
	PacketsLost           uint64 `json:"packets_lost"`
	SpuriousLossPackets   uint64 `json:"spurious_loss_packets"`
	QueueDrops            uint64 `json:"queue_drops"`
	ReceiveQueueOverflow  uint64 `json:"receive_queue_overflow"`
	FECPolicy             string `json:"fec_policy,omitempty"`
	Endpoint              string `json:"endpoint,omitempty"`
}

// SessionEvent is globally sequenced within EventStreamID. SupervisorEpoch is
// attached by the portable quick management layer and intentionally omitted
// from the core-owned record.
type SessionEvent struct {
	TelemetryVersion   int                  `json:"telemetry_version"`
	EventStreamID      string               `json:"event_stream_id"`
	EventSequence      uint64               `json:"event_sequence"`
	SessionID          uint64               `json:"session_id"`
	SessionGeneration  uint64               `json:"session_generation"`
	EventType          string               `json:"event_type"`
	Reason             string               `json:"reason,omitempty"`
	MonotonicElapsedNS int64                `json:"monotonic_elapsed_ns"`
	WallTime           time.Time            `json:"wall_time"`
	Before             *SessionEventMetrics `json:"before,omitempty"`
	After              *SessionEventMetrics `json:"after,omitempty"`
}

// SessionEventBatch is the cursor response for events after a known sequence.
// LastSequence is the next cursor a client should pass as after_sequence.
type SessionEventBatch struct {
	TelemetryVersion       int            `json:"telemetry_version"`
	SupervisorEpoch        string         `json:"supervisor_epoch,omitempty"`
	EventStreamID          string         `json:"event_stream_id"`
	SampledAt              time.Time      `json:"sampled_at"`
	MonotonicElapsedNS     int64          `json:"monotonic_elapsed_ns"`
	FirstAvailableSequence uint64         `json:"first_available_sequence,omitempty"`
	LastSequence           uint64         `json:"last_sequence,omitempty"`
	EventsDroppedTotal     uint64         `json:"events_dropped_total,omitempty"`
	Events                 []SessionEvent `json:"events,omitempty"`
}
