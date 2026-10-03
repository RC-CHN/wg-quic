package ackhandler

import (
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/congestion"
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/stretchr/testify/require"
)

type ecnModelClock struct{ now monotime.Time }

func (c *ecnModelClock) Now() monotime.Time { return c.now }

func TestModelECNTrackerDoesNotCompoundOneFlight(t *testing.T) {
	clock := &ecnModelClock{now: monotime.Now()}
	rtt := utils.NewRTTStats()
	rtt.UpdateRTT(40*time.Millisecond, 0)
	stats := &utils.ConnectionStats{}
	sender := congestion.NewModelSender(clock, rtt, stats, 1200, nil)
	tracker := newECNTracker(utils.DefaultLogger, nil)
	send := func(pn protocol.PacketNumber) {
		mode := tracker.Mode()
		require.Equal(t, protocol.ECT0, mode)
		tracker.SentPacket(pn, mode)
		sender.OnPacketSent(clock.Now(), 120000, pn, 1200, true)
	}
	for pn := protocol.PacketNumber(0); pn < 10; pn++ {
		send(pn)
	}
	require.False(t, tracker.HandleNewlyAcked(getAckedPackets(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), 10, 0, 0))
	require.Equal(t, ecnStateCapable, tracker.state)
	for pn := protocol.PacketNumber(10); pn < 110; pn++ {
		send(pn)
	}
	before := sender.Stats().BandwidthEstimate
	for pn := protocol.PacketNumber(10); pn < 110; pn++ {
		if pn == 60 {
			clock.now = clock.now.Add(120 * time.Millisecond)
		}
		// Each ACK advances LargestAcked and CE count exactly once. The real
		// tracker must report every increase; the controller owns recovery.
		require.True(t, tracker.HandleNewlyAcked(getAckedPackets(pn), 10, 0, int64(pn-9)))
		sender.OnCongestionEvent(pn, 0, 120000)
	}
	require.Equal(t, before*3/4, sender.Stats().BandwidthEstimate,
		"one flight's CE feedback must not compound, including feedback after an RTT")
	require.Zero(t, stats.PacketsLost.Load(), "CE feedback is not packet loss")
	first := sender.Stats().BandwidthEstimate
	clock.now = clock.now.Add(40 * time.Millisecond)
	send(110)
	require.True(t, tracker.HandleNewlyAcked(getAckedPackets(110), 10, 0, 101))
	sender.OnCongestionEvent(110, 0, 1200)
	require.Less(t, sender.Stats().BandwidthEstimate, first, "new flight CE must still reduce without a queue predicate")
}
