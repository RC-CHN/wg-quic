package telemetry

import "time"

// SessionTelemetryVersion is the schema version for SessionObservation.
// Additive JSON fields do not require a version bump. Change it when field
// semantics or counter lifetimes become incompatible.
const SessionTelemetryVersion = 1

// RecentSessionTelemetryVersion is the schema version for final observations
// retained after a session leaves the active set.
const RecentSessionTelemetryVersion = 1

// Session close reasons are stable protocol values. Callers must treat values
// they don't recognize as unknown instead of deriving behavior from error text.
const (
	SessionCloseLocalShutdown        = "local_shutdown"
	SessionCloseRemote               = "remote_close"
	SessionCloseIdleTimeout          = "idle_timeout"
	SessionCloseHandshakeTimeout     = "handshake_timeout"
	SessionCloseTransportError       = "transport_error"
	SessionCloseEndpointReplaced     = "endpoint_replaced"
	SessionCloseConfigurationRemoved = "configuration_removed"
	SessionCloseUnknown              = "unknown"
)

// SessionPeerObservation describes why a QUIC session is associated with a
// WireGuard peer. Multiple peers may legitimately share one outer endpoint,
// so session telemetry must not assume a one-to-one relationship.
type SessionPeerObservation struct {
	PublicKey          string `json:"public_key"`
	EndpointGeneration uint64 `json:"endpoint_generation,omitempty"`
	Configured         bool   `json:"configured,omitempty"`
	Authenticated      bool   `json:"authenticated,omitempty"`
}

// SessionObservation is a point-in-time observation of one QUIC connection.
// SessionID is unique for the lifetime of one core process. SessionGeneration
// increases when a configured endpoint creates a replacement connection.
type SessionObservation struct {
	TelemetryVersion   int                      `json:"telemetry_version"`
	SessionID          uint64                   `json:"session_id"`
	SessionGeneration  uint64                   `json:"session_generation"`
	Role               string                   `json:"role"`
	State              string                   `json:"state"`
	ConfiguredEndpoint string                   `json:"configured_endpoint,omitempty"`
	CurrentEndpoint    string                   `json:"current_endpoint,omitempty"`
	EstablishedAt      *time.Time               `json:"established_at,omitempty"`
	SampledAt          time.Time                `json:"sampled_at"`
	Peers              []SessionPeerObservation `json:"peers,omitempty"`
	ReplacesSessionID  uint64                   `json:"replaces_session_id,omitempty"`
	Stats              SessionStats             `json:"stats"`
}

// ClosedSessionObservation is the immutable final observation of a session.
// FinalSequence is monotonic for one core event stream and lets a polling
// collector de-duplicate retained records and detect an eviction gap.
type ClosedSessionObservation struct {
	TelemetryVersion    int                      `json:"telemetry_version"`
	FinalSequence       uint64                   `json:"final_sequence"`
	SessionID           uint64                   `json:"session_id"`
	SessionGeneration   uint64                   `json:"session_generation"`
	Role                string                   `json:"role"`
	State               string                   `json:"state"`
	ConfiguredEndpoint  string                   `json:"configured_endpoint,omitempty"`
	CurrentEndpoint     string                   `json:"current_endpoint,omitempty"`
	EstablishedAt       *time.Time               `json:"established_at,omitempty"`
	ClosedAt            time.Time                `json:"closed_at"`
	SampledAt           time.Time                `json:"sampled_at"`
	Peers               []SessionPeerObservation `json:"peers,omitempty"`
	CloseReason         string                   `json:"close_reason"`
	ErrorClass          string                   `json:"error_class,omitempty"`
	LastError           string                   `json:"last_error,omitempty"`
	ReplacedBySessionID uint64                   `json:"replaced_by_session_id,omitempty"`
	ReplacesSessionID   uint64                   `json:"replaces_session_id,omitempty"`
	Final               bool                     `json:"final"`
	FinalStats          SessionStats             `json:"final_stats"`
}

// SessionStats contains connection-scoped counters and gauges. Counters start
// at session creation and disappear when the session leaves the active status
// set; callers must key deltas by supervisor epoch, SessionID, and generation.
type SessionStats struct {
	WGTxPackets   uint64 `json:"wg_tx_packets"`
	WGTxBytes     uint64 `json:"wg_tx_bytes"`
	WGRxPackets   uint64 `json:"wg_rx_packets"`
	WGRxBytes     uint64 `json:"wg_rx_bytes"`
	WireTxPackets uint64 `json:"wire_tx_packets"`
	WireTxBytes   uint64 `json:"wire_tx_bytes"`
	WireRxPackets uint64 `json:"wire_rx_packets"`
	WireRxBytes   uint64 `json:"wire_rx_bytes"`
	QueueDrops    uint64 `json:"queue_drops"`

	SendQueueDepth     uint64 `json:"send_queue_depth"`
	PriorityQueueDepth uint64 `json:"priority_queue_depth"`
	ControlQueueDepth  uint64 `json:"control_queue_depth"`

	FECDataTx              uint64 `json:"fec_data_tx"`
	FECParityTx            uint64 `json:"fec_parity_tx"`
	FECRawLost             uint64 `json:"fec_raw_lost"`
	FECRecovered           uint64 `json:"fec_recovered"`
	FECUnrecovered         uint64 `json:"fec_unrecovered"`
	FECCurrentParityShards uint64 `json:"fec_current_parity_shards"`
	FECLossEstimatePPM     uint64 `json:"fec_loss_estimate_ppm"`

	QUICBytesSent             uint64 `json:"quic_bytes_sent"`
	QUICPacketsSent           uint64 `json:"quic_packets_sent"`
	QUICBytesReceived         uint64 `json:"quic_bytes_received"`
	QUICPacketsReceived       uint64 `json:"quic_packets_received"`
	QUICBytesAcked            uint64 `json:"quic_bytes_acked"`
	QUICPacketsAcked          uint64 `json:"quic_packets_acked"`
	QUICBytesLost             uint64 `json:"quic_bytes_lost"`
	QUICPacketsLost           uint64 `json:"quic_packets_lost"`
	QUICSpuriousLossPackets   uint64 `json:"quic_spurious_loss_packets"`
	QUICPTOCount              uint64 `json:"quic_pto_count"`
	QUICMinRTTUs              uint64 `json:"quic_min_rtt_us"`
	QUICLatestRTTUs           uint64 `json:"quic_latest_rtt_us"`
	QUICSmoothedRTTUs         uint64 `json:"quic_smoothed_rtt_us"`
	QUICRTTVarUs              uint64 `json:"quic_rttvar_us"`
	QUICCongestionWindowBytes uint64 `json:"quic_congestion_window_bytes"`
	QUICBytesInFlight         uint64 `json:"quic_bytes_in_flight"`
	QUICBandwidthEstimateBps  uint64 `json:"quic_bandwidth_estimate_bps"`
	QUICPacingRateBps         uint64 `json:"quic_pacing_rate_bps"`
	QUICPathRTTUs             uint64 `json:"quic_path_rtt_us"`
	QUICQueueDelayUs          uint64 `json:"quic_queue_delay_us"`
	QUICFECRecoverableLossPPM uint64 `json:"quic_fec_recoverable_loss_ppm"`
	QUICFECResidualLossPPM    uint64 `json:"quic_fec_residual_loss_ppm"`
	QUICCongestionModelState  uint64 `json:"quic_congestion_model_state"`
	QUICDatagramSendQueueLen  uint64 `json:"quic_datagram_send_queue_len"`
}
