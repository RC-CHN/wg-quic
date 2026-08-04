// Package telemetry defines transport-independent runtime observations shared
// by the data plane and its local control clients.
package telemetry

// Stats is a point-in-time snapshot of the userspace data plane counters.
//
// Keep this package free of bind, carrier, and operating-system dependencies so
// management clients can consume status without compiling the data plane.
type Stats struct {
	WGTxPackets            uint64 `json:"wg_tx_packets"`
	WGTxBytes              uint64 `json:"wg_tx_bytes"`
	WGRxPackets            uint64 `json:"wg_rx_packets"`
	WGRxBytes              uint64 `json:"wg_rx_bytes"`
	WireTxPackets          uint64 `json:"wire_tx_packets"`
	WireTxBytes            uint64 `json:"wire_tx_bytes"`
	WireRxPackets          uint64 `json:"wire_rx_packets"`
	WireRxBytes            uint64 `json:"wire_rx_bytes"`
	QueueDrops             uint64 `json:"queue_drops"`
	FECDataTx              uint64 `json:"fec_data_tx"`
	FECParityTx            uint64 `json:"fec_parity_tx"`
	FECRawLost             uint64 `json:"fec_raw_lost"`
	FECRecovered           uint64 `json:"fec_recovered"`
	FECUnrecovered         uint64 `json:"fec_unrecovered"`
	FECCurrentParityShards uint64 `json:"fec_current_parity_shards"`
	FECLossEstimatePPM     uint64 `json:"fec_loss_estimate_ppm"`
	ActiveSessions         uint64 `json:"active_sessions"`

	QUICBytesAcked            uint64 `json:"quic_bytes_acked"`
	QUICPacketsAcked          uint64 `json:"quic_packets_acked"`
	QUICBytesLost             uint64 `json:"quic_bytes_lost"`
	QUICPacketsLost           uint64 `json:"quic_packets_lost"`
	QUICMinRTTUs              uint64 `json:"quic_min_rtt_us"`
	QUICSmoothedRTTUs         uint64 `json:"quic_smoothed_rtt_us"`
	QUICLatestRTTUs           uint64 `json:"quic_latest_rtt_us"`
	QUICCongestionWindowBytes uint64 `json:"quic_congestion_window_bytes"`
	QUICBytesInFlight         uint64 `json:"quic_bytes_in_flight"`
	QUICBandwidthEstimateBps  uint64 `json:"quic_bandwidth_estimate_bps"`
	QUICPacingRateBps         uint64 `json:"quic_pacing_rate_bps"`
	QUICPathRTTUs             uint64 `json:"quic_path_rtt_us"`
	QUICQueueDelayUs          uint64 `json:"quic_queue_delay_us"`
	QUICFECRecoverableLossPPM uint64 `json:"quic_fec_recoverable_loss_ppm"`
	QUICFECResidualLossPPM    uint64 `json:"quic_fec_residual_loss_ppm"`
	QUICCongestionModelState  uint64 `json:"quic_congestion_model_state"`
}
