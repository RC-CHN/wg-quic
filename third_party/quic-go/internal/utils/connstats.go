package utils

import "sync/atomic"

// ConnectionStats stores stats for the connection. See the public
// ConnectionStats struct in connection.go for more information
type ConnectionStats struct {
	BytesSent             atomic.Uint64
	PacketsSent           atomic.Uint64
	BytesAcked            atomic.Uint64
	PacketsAcked          atomic.Uint64
	BytesReceived         atomic.Uint64
	PacketsReceived       atomic.Uint64
	BytesLost             atomic.Uint64
	PacketsLost           atomic.Uint64
	CongestionWindow      atomic.Uint64
	BytesInFlight         atomic.Uint64
	BandwidthEstimate     atomic.Uint64
	PacingRate            atomic.Uint64
	PropagationRTT        atomic.Int64
	QueueDelay            atomic.Int64
	FECObservedData       atomic.Uint64
	FECObservedMissing    atomic.Uint64
	FECObservedRecovered  atomic.Uint64
	FECRecoverableLossPPM atomic.Uint64
	FECResidualLossPPM    atomic.Uint64
	CongestionModelState  atomic.Uint64
}
