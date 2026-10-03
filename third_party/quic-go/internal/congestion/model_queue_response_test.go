package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/stretchr/testify/require"
)

func queuedModelFlight() (*modelSender, *mockClock) {
	m, clock, rtt, _ := newTestModelSender()
	m.state = modelStateProbe
	m.bandwidthEstimate = 80_000_000
	m.congestionWindow = m.targetCongestionWindow()
	for range 20 {
		rtt.UpdateRTT(120*time.Millisecond, 0)
	}
	for pn := protocol.PacketNumber(1); pn <= 100; pn++ {
		m.OnPacketSent(clock.Now(), 80_000, pn, 1200, true)
	}
	return m, clock
}

func TestModelQueuedLossDoesNotCompoundWithinOneFlight(t *testing.T) {
	m, clock := queuedModelFlight()
	m.OnCongestionEvent(1, 1200, 80_000)
	want := m.bandwidthEstimate
	require.Equal(t, Bandwidth(68_000_000), want)
	for pn := protocol.PacketNumber(2); pn <= 100; pn++ {
		if pn == 50 {
			clock.Advance(3 * m.modelRTT())
		}
		m.OnCongestionEvent(pn, 1200, 80_000)
	}
	require.Equal(t, want, m.bandwidthEstimate, "late loss from the old flight is not new congestion evidence")
	require.Equal(t, uint64(100), m.connStats.PacketsLost.Load(), "suppress repeated reduction, not loss telemetry")
}

func TestModelQueueAndLossShareOneResponseRegardlessOfOrder(t *testing.T) {
	for _, lossFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "ack-first", true: "loss-first"}[lossFirst], func(t *testing.T) {
			m, clock := queuedModelFlight()
			ack := func(pn protocol.PacketNumber) { m.OnPacketAcked(pn, 1200, 20_000, clock.Now()) }
			if lossFirst {
				m.OnCongestionEvent(1, 1200, 80_000)
			} else {
				ack(1)
			}
			want := m.bandwidthEstimate
			if lossFirst {
				ack(2)
			} else {
				m.OnCongestionEvent(2, 1200, 80_000)
			}
			require.Equal(t, want, m.bandwidthEstimate)
			clock.Advance(3 * m.modelRTT())
			ack(99)
			require.Equal(t, want, m.bandwidthEstimate, "old-flight ACK cannot duplicate a loss response after a timer expires")
		})
	}
}

func TestModelNewFlightCanStillRespondToPersistentCongestion(t *testing.T) {
	m, clock := queuedModelFlight()
	m.OnCongestionEvent(1, 1200, 80_000)
	first := m.bandwidthEstimate
	clock.Advance(m.modelRTT())
	m.OnPacketSent(clock.Now(), 20_000, 101, 1200, true)
	m.OnPacketAcked(101, 1200, 20_000, clock.Now())
	require.Less(t, m.bandwidthEstimate, first)
	second := m.bandwidthEstimate
	m.OnPacketSent(clock.Now(), 20_000, 102, 1200, true)
	m.OnCongestionEvent(102, 1200, 20_000)
	require.Equal(t, second, m.bandwidthEstimate, "new packets still respect the feedback interval")
	clock.Advance(m.modelRTT())
	m.OnPacketSent(clock.Now(), 20_000, 103, 1200, true)
	m.OnCongestionEvent(103, 1200, 20_000)
	require.Less(t, m.bandwidthEstimate, second)
}

func TestModelECNAndQueuedLossShareRecovery(t *testing.T) {
	for _, ecnFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "loss-first", true: "ecn-first"}[ecnFirst], func(t *testing.T) {
			m, _ := queuedModelFlight()
			firstBytes, secondBytes := protocol.ByteCount(1200), protocol.ByteCount(0)
			if ecnFirst {
				firstBytes, secondBytes = secondBytes, firstBytes
			}
			m.OnCongestionEvent(1, firstBytes, 80_000)
			first := m.bandwidthEstimate
			m.OnCongestionEvent(2, secondBytes, 80_000)
			require.Equal(t, first, m.bandwidthEstimate)
			require.Equal(t, uint64(1), m.connStats.PacketsLost.Load())
		})
	}
}

func TestModelNewFlightECNBypassesRTTQueueCalibration(t *testing.T) {
	m, clock, rtt, _ := newTestModelSender()
	m.queueSignalResume = clock.Now().Add(time.Hour)
	m.OnPacketSent(clock.Now(), 20_000, 1, 1200, true)
	before := m.bandwidthEstimate
	m.OnCongestionEvent(1, 0, 20_000)
	require.Less(t, m.bandwidthEstimate, before, "explicit ECN does not need a queue predicate")
	first := m.bandwidthEstimate
	clock.Advance(time.Millisecond)
	require.Less(t, time.Millisecond, m.modelRTT())
	m.OnPacketSent(clock.Now(), 20_000, 2, 1200, true)
	// The ACK handler updates raw RTT before delivering CE, but the model
	// observes that path change only in the later OnPacketAcked callback.
	rtt.UpdateRTT(time.Millisecond, 0)
	m.OnCongestionEvent(2, 0, 20_000)
	require.Less(t, m.bandwidthEstimate, first, "new-flight CE is evidence even before a stale RTT timer")
}
