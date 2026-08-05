package congestion

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"

	"github.com/stretchr/testify/require"
)

func newTestModelSender() (*modelSender, *mockClock, *utils.RTTStats, *utils.ConnectionStats) {
	clock := mockClock(monotime.Time(time.Second))
	rttStats := &utils.RTTStats{}
	rttStats.UpdateRTT(40*time.Millisecond, 0)
	connStats := &utils.ConnectionStats{}
	sender := NewModelSender(
		&clock,
		rttStats,
		connStats,
		protocol.InitialPacketSize,
		nil,
	)
	return sender, &clock, rttStats, connStats
}

func ackModelWindow(
	sender *modelSender,
	clock *mockClock,
	firstPacket protocol.PacketNumber,
) protocol.PacketNumber {
	priorInFlight := sender.GetCongestionWindow()
	packetCount := int(priorInFlight / sender.maxDatagramSize)
	for i := range packetCount {
		packetNumber := firstPacket + protocol.PacketNumber(i)
		sender.OnPacketSent(clock.Now(), priorInFlight, packetNumber, sender.maxDatagramSize, true)
		sender.OnPacketAcked(packetNumber, sender.maxDatagramSize, priorInFlight, clock.Now())
	}
	return firstPacket + protocol.PacketNumber(packetCount)
}

func TestModelSenderLearnsDeliveryCapacity(t *testing.T) {
	sender, clock, _, _ := newTestModelSender()
	initialBandwidth := sender.Stats().BandwidthEstimate
	nextPacket := ackModelWindow(sender, clock, 1)
	clock.Advance(10 * time.Millisecond)
	ackModelWindow(sender, clock, nextPacket)

	require.Greater(t, sender.Stats().BandwidthEstimate, initialBandwidth)
	require.Greater(t, sender.GetCongestionWindow(), protocol.ByteCount(initialCongestionWindow)*protocol.InitialPacketSize)
	require.Greater(t, sender.Stats().PacingRate, sender.Stats().BandwidthEstimate)
}

func TestModelSenderUsesFasterApplicationLimitedSample(t *testing.T) {
	sender, clock, _, _ := newTestModelSender()
	sender.congestionWindow = 400_000
	sender.bandwidthEstimate = Bandwidth(1_000_000)
	priorInFlight := protocol.ByteCount(100_000)

	sender.observeDelivery(50_000, priorInFlight, clock.Now())
	clock.Advance(10 * time.Millisecond)
	sender.observeDelivery(50_000, priorInFlight, clock.Now())

	require.Less(t, priorInFlight, sender.congestionWindow/2)
	require.Greater(t, sender.bandwidthEstimate, Bandwidth(1_000_000))
}

func TestModelSenderDoesNotExitStartupOnApplicationLimitedRounds(t *testing.T) {
	sender, _, _, _ := newTestModelSender()
	sender.roundEnd = 1

	for packetNumber := protocol.PacketNumber(2); packetNumber <= 8; packetNumber++ {
		sender.largestSent = packetNumber
		sender.updateRound(packetNumber, false)
	}

	require.True(t, sender.InSlowStart())
	require.Zero(t, sender.fullBandwidthRounds)
	require.Zero(t, sender.fullBandwidth)
}

func TestModelSenderExitsStartupOnCapacityLimitedPlateau(t *testing.T) {
	sender, _, _, _ := newTestModelSender()
	sender.roundEnd = 1

	for packetNumber := protocol.PacketNumber(2); packetNumber <= 5; packetNumber++ {
		sender.largestSent = packetNumber
		sender.updateRound(packetNumber, true)
	}

	require.False(t, sender.InSlowStart())
	require.Equal(t, 3, sender.fullBandwidthRounds)
	require.NotZero(t, sender.fullBandwidth)
}

func TestModelSenderIgnoresIsolatedRandomLossWithoutQueue(t *testing.T) {
	sender, _, _, _ := newTestModelSender()
	before := sender.Stats()
	sender.OnCongestionEvent(1, protocol.InitialPacketSize, before.CongestionWindow)
	after := sender.Stats()

	require.Equal(t, before.CongestionWindow, after.CongestionWindow)
	require.Equal(t, before.BandwidthEstimate, after.BandwidthEstimate)
}

func TestModelSenderRespondsToQueuedLoss(t *testing.T) {
	sender, _, rttStats, _ := newTestModelSender()
	for range 8 {
		rttStats.UpdateRTT(120*time.Millisecond, 0)
	}
	before := sender.Stats()
	sender.OnCongestionEvent(1, protocol.InitialPacketSize, before.CongestionWindow)
	after := sender.Stats()

	require.Less(t, after.BandwidthEstimate, before.BandwidthEstimate)
	require.LessOrEqual(t, after.CongestionWindow, before.CongestionWindow)
	require.Greater(t, after.QueueDelay, time.Duration(0))
}

func TestModelSenderUsesFECRecoveryOutcome(t *testing.T) {
	t.Run("recoverable loss does not cut the model", func(t *testing.T) {
		sender, clock, _, stats := newTestModelSender()
		stats.FECObservedData.Add(100)
		stats.FECObservedMissing.Add(10)
		stats.FECObservedRecovered.Add(10)
		sender.OnPacketAcked(1, protocol.InitialPacketSize, sender.GetCongestionWindow(), clock.Now())
		before := sender.Stats()
		sender.OnCongestionEvent(2, protocol.InitialPacketSize, before.CongestionWindow)
		after := sender.Stats()

		require.Equal(t, uint64(100_000), after.FECRecoverableLossPPM)
		require.Zero(t, after.FECResidualLossPPM)
		require.Equal(t, before.BandwidthEstimate, after.BandwidthEstimate)
	})

	t.Run("residual random loss is delegated to protection", func(t *testing.T) {
		sender, clock, _, stats := newTestModelSender()
		stats.FECObservedData.Add(100)
		stats.FECObservedMissing.Add(10)
		sender.OnPacketAcked(1, protocol.InitialPacketSize, sender.GetCongestionWindow(), clock.Now())
		before := sender.Stats()
		sender.OnCongestionEvent(2, protocol.InitialPacketSize, before.CongestionWindow)
		after := sender.Stats()

		require.Equal(t, uint64(100_000), after.FECResidualLossPPM)
		require.Equal(t, before.BandwidthEstimate, after.BandwidthEstimate)
	})
}

func TestModelSenderUpdatesPropagationRTTAfterPathLatencyIncrease(t *testing.T) {
	sender, clock, rttStats, _ := newTestModelSender()
	sender.congestionWindow = sender.minCongestionWindow()

	rttStats.UpdateRTT(80*time.Millisecond, 0)
	sender.OnPacketAcked(1, protocol.InitialPacketSize, sender.congestionWindow, clock.Now())
	clock.Advance(120 * time.Millisecond)
	rttStats.UpdateRTT(75*time.Millisecond, 0)
	sender.OnPacketAcked(2, protocol.InitialPacketSize, sender.congestionWindow, clock.Now())

	require.Equal(t, 75*time.Millisecond, sender.modelRTT())
	require.False(t, sender.hasStandingQueue(clock.Now(), modelQueueThreshold))
}

func TestModelSenderRejectsTransientImplausibleRTTDrop(t *testing.T) {
	sender, clock, rttStats, _ := newTestModelSender()

	rttStats.UpdateRTT(100*time.Microsecond, 0)
	sender.OnPacketAcked(1, protocol.InitialPacketSize, sender.congestionWindow, clock.Now())
	clock.Advance(10 * time.Millisecond)
	rttStats.UpdateRTT(40*time.Millisecond, 0)
	sender.OnPacketAcked(2, protocol.InitialPacketSize, sender.congestionWindow, clock.Now())

	require.Equal(t, 40*time.Millisecond, sender.modelRTT())
}

func TestModelSenderConfirmsSustainedLargeRTTDrop(t *testing.T) {
	sender, clock, rttStats, _ := newTestModelSender()
	sender.propagationRTT = 80 * time.Millisecond

	rttStats.UpdateRTT(10*time.Millisecond, 0)
	sender.OnPacketAcked(1, protocol.InitialPacketSize, sender.congestionWindow, clock.Now())
	clock.Advance(60 * time.Millisecond)
	rttStats.UpdateRTT(11*time.Millisecond, 0)
	sender.OnPacketAcked(2, protocol.InitialPacketSize, sender.congestionWindow, clock.Now())

	require.Equal(t, 10*time.Millisecond, sender.modelRTT())
	require.False(t, sender.hasStandingQueue(clock.Now(), modelQueueThreshold))
}
