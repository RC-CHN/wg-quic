// Package telemetry defines transport-independent runtime observations shared
// by the data plane and its local control clients.
package telemetry

// Stats is a point-in-time snapshot of the userspace data plane counters.
//
// Keep this package free of bind, carrier, and operating-system dependencies so
// management clients can consume status without compiling the data plane.
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
