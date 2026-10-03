package congestion

import (
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCongestionResponseNeverRaisesLearnedSlowRate(t *testing.T) {
	m, _, _, _ := newTestModelSender()
	m.bandwidthEstimate = 64_000
	m.reduceModel(.85)
	require.Equal(t, Bandwidth(54_400), m.bandwidthEstimate)
	require.GreaterOrEqual(t, m.congestionWindow, m.minCongestionWindow())
}

func TestSerializationIsNotStandingQueue(t *testing.T) {
	m, clock, rtt, _ := newTestModelSender()
	m.bandwidthEstimate = 64_000
	for range 20 {
		rtt.UpdateRTT(100*time.Millisecond, 0)
	}
	require.False(t, m.hasStandingQueue(clock.Now(), modelQueueThreshold))
	for range 20 {
		rtt.UpdateRTT(time.Second, 0)
	}
	require.True(t, m.hasStandingQueue(clock.Now(), modelQueueThreshold))
}

func TestPathBaselineWaitsForOldFlightToDrain(t *testing.T) {
	m, clock, rtt, _ := newTestModelSender()
	m.congestionWindow = m.minCongestionWindow()
	m.largestSent = 100
	for n := protocol.PacketNumber(1); n < 10; n++ {
		clock.Advance(100 * time.Millisecond)
		rtt.UpdateRTT(400*time.Millisecond, 0)
		m.observePathRTT(clock.Now(), n, 400_000)
	}
	require.Equal(t, 40*time.Millisecond, m.modelRTT(), "small window did not drain the old large flight")
	for n := protocol.PacketNumber(90); n < 100; n++ {
		clock.Advance(100 * time.Millisecond)
		rtt.UpdateRTT(400*time.Millisecond, 0)
		m.observePathRTT(clock.Now(), n, m.minCongestionWindow())
	}
	require.Equal(t, 40*time.Millisecond, m.modelRTT(), "old-flight ACKs cannot establish a new baseline")
	rtt.UpdateRTT(80*time.Millisecond, 0)
	m.observePathRTT(clock.Now(), 101, m.minCongestionWindow())
	clock.Advance(120 * time.Millisecond)
	rtt.UpdateRTT(75*time.Millisecond, 0)
	m.observePathRTT(clock.Now(), 102, m.minCongestionWindow())
	require.Equal(t, 75*time.Millisecond, m.modelRTT(), "genuine path changes still converge")
}

func TestLargeCapacityDropRetiresPeakOnlyAfterRepeatedLoadedSamples(t *testing.T) {
	m, clock, rtt, _ := newTestModelSender()
	m.congestionWindow = 500_000
	m.recordBandwidthSample(100_000_000)
	for range 20 {
		rtt.UpdateRTT(200*time.Millisecond, 0)
	}
	m.observeDelivery(1200, 400_000, clock.Now())
	for i := 0; i < 3; i++ {
		clock.Advance(50 * time.Millisecond)
		m.observeDelivery(6000, 400_000, clock.Now())
		if i < 2 {
			require.Equal(t, Bandwidth(100_000_000), m.bandwidthEstimate)
		}
	}
	require.Less(t, m.bandwidthEstimate, Bandwidth(2_000_000))
	require.Less(t, m.congestionWindow, protocol.ByteCount(25_000))
	// Recovery remains discoverable, even with application-limited flight.
	clock.Advance(50 * time.Millisecond)
	m.observeDelivery(600_000, 500_000, clock.Now())
	require.Greater(t, m.bandwidthEstimate, Bandwidth(50_000_000))
}
