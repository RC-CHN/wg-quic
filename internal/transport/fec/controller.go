package fec

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultBypassLossThreshold = 0.001
	minBypassLossThreshold     = 0.0001
	bypassReferenceRTT         = 100 * time.Millisecond
	defaultDecreaseGroups      = 32
	maxDecreaseGroups          = 256
)

type Feedback struct {
	Epoch     uint16
	GroupID   uint64
	Missing   uint16
	Recovered uint16
	Total     uint16
}

type Controller struct {
	mu sync.Mutex

	parity            int
	paritySnapshot    atomic.Int32
	lossSnapshotPPM   atomic.Uint64
	zeroLossGroups    int
	observedFrames    int
	decreaseGroups    int
	unrecoveredGroups int
	lossEWMA          float64
	lossInitialized   bool
	pathRTT           time.Duration

	transportInitialized bool
	lastTransportSent    uint64
	lastTransportLost    uint64
}

func NewController() *Controller {
	controller := &Controller{parity: 1, lossInitialized: true}
	controller.paritySnapshot.Store(1)
	return controller
}

func (c *Controller) Parity(k int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k <= 0 {
		return 0
	}
	return min(c.parity, max(1, k/2))
}

// Observe updates the parity target and reports whether it changed.
func (c *Controller) Observe(feedback Feedback) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.storeSnapshotsLocked()
	previous := c.parity
	if feedback.Total == 0 {
		return false
	}
	sample := float64(min(feedback.Missing, feedback.Total)) / float64(feedback.Total)
	alpha := min(0.25, max(0.03125, float64(feedback.Total)/256))
	c.updateLossLocked(sample, alpha)
	c.observedFrames += int(feedback.Total)
	// A zero-parity sender periodically emits a protected probe group. Any
	// missing shard in that sample is enough to leave the healthy fast path.
	// Recovery proves that the path needs protection even if it hid the loss
	// from the application.
	if c.parity == 0 && feedback.Missing > 0 {
		c.parity = 1
		c.zeroLossGroups = 0
		c.observedFrames = 0
		c.decreaseGroups = 0
		return true
	}
	if feedback.Missing == 0 {
		c.zeroLossGroups++
	} else {
		c.zeroLossGroups = 0
	}

	desired := c.desiredParityLocked(DefaultDataShards)
	if feedback.Missing > feedback.Recovered {
		c.unrecoveredGroups++
		if c.parity < desired ||
			feedback.Missing-feedback.Recovered >= 2 ||
			c.unrecoveredGroups >= 3 {
			c.parity = min(c.parity+1, 4)
			c.unrecoveredGroups = 0
		}
		c.observedFrames = 0
		c.decreaseGroups = 0
		return c.parity != previous
	}
	c.unrecoveredGroups = 0
	decreaseWindow := c.decreaseWindowLocked(desired)
	if c.zeroLossGroups >= decreaseWindow && c.parity > desired {
		c.parity--
		c.zeroLossGroups = 0
		c.decreaseGroups = 0
		return c.parity != previous
	}

	switch {
	case desired > c.parity && feedback.Missing > 0 && c.observedFrames >= 32:
		c.parity++
		c.observedFrames = 0
		c.decreaseGroups = 0
	case desired < c.parity:
		c.decreaseGroups++
		if c.decreaseGroups >= decreaseWindow {
			c.parity--
			c.decreaseGroups = 0
		}
	default:
		c.decreaseGroups = 0
	}
	return c.parity != previous
}

func (c *Controller) CurrentParity() int {
	return int(c.paritySnapshot.Load())
}

func (c *Controller) setParity(parity int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parity = parity
	c.paritySnapshot.Store(int32(parity))
}

// ObservePathRTT adjusts how much measured loss is acceptable before enabling
// protection. A loss on a long-RTT path stalls the encapsulated transport for
// much longer, so the healthy-path bypass threshold scales down with RTT.
func (c *Controller) ObservePathRTT(rtt time.Duration) {
	if rtt <= 0 {
		return
	}
	c.mu.Lock()
	c.pathRTT = rtt
	c.mu.Unlock()
}

// ObserveTransport supplements sparse FEC probe feedback with QUIC's
// continuously measured packet-loss counters. It is especially useful while
// parity is zero and low-rate traffic would otherwise take a long time to
// reach the next protected probe group.
func (c *Controller) ObserveTransport(packetsSent, packetsLost uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.storeSnapshotsLocked()
	if !c.transportInitialized || packetsSent < c.lastTransportSent || packetsLost < c.lastTransportLost {
		c.transportInitialized = true
		c.lastTransportSent = packetsSent
		c.lastTransportLost = packetsLost
		return false
	}
	sent := packetsSent - c.lastTransportSent
	if sent < 128 {
		return false
	}
	lost := min(packetsLost-c.lastTransportLost, sent)
	c.lastTransportSent = packetsSent
	c.lastTransportLost = packetsLost
	sample := float64(lost) / float64(sent)
	c.updateLossLocked(sample, min(0.25, max(0.0625, float64(sent)/256)))
	previous := c.parity
	desired := c.desiredParityLocked(DefaultDataShards)
	switch {
	case c.parity == 0 && lost >= 2 && sample >= 0.005:
		c.parity = 1
		c.zeroLossGroups = 0
	case desired > c.parity:
		c.parity++
		c.decreaseGroups = 0
	}
	return c.parity != previous
}

func (c *Controller) LossEstimatePPM() uint64 {
	return c.lossSnapshotPPM.Load()
}

func (c *Controller) updateLossLocked(sample, alpha float64) {
	if !c.lossInitialized {
		c.lossEWMA = sample
		c.lossInitialized = true
		return
	}
	c.lossEWMA = (1-alpha)*c.lossEWMA + alpha*sample
}

func (c *Controller) storeSnapshotsLocked() {
	c.paritySnapshot.Store(int32(c.parity))
	c.lossSnapshotPPM.Store(uint64(min(1.0, max(0.0, c.lossEWMA))*1_000_000 + 0.5))
}

func (c *Controller) desiredParityLocked(dataShards int) int {
	threshold := defaultBypassLossThreshold
	if c.pathRTT > bypassReferenceRTT {
		threshold *= float64(bypassReferenceRTT) / float64(c.pathRTT)
		threshold = max(minBypassLossThreshold, threshold)
	}
	return parityForLossThreshold(dataShards, c.lossEWMA, threshold)
}

func (c *Controller) decreaseWindowLocked(desired int) int {
	// Long-RTT confidence is needed for the expensive 1 -> 0 decision. It
	// should not keep surplus protection after a transient burst.
	if c.parity != 1 || desired != 0 || c.pathRTT <= bypassReferenceRTT {
		return defaultDecreaseGroups
	}
	rttMultiple := int(
		(c.pathRTT + bypassReferenceRTT - 1) / bypassReferenceRTT,
	)
	return min(maxDecreaseGroups, defaultDecreaseGroups*rttMultiple)
}

// parityForLoss selects the smallest repair count whose independent-loss
// group failure probability is at most 0.5%. The runtime controller adds
// hysteresis and reacts faster to observed unrecovered groups.
func parityForLoss(dataShards int, loss float64) int {
	return parityForLossThreshold(dataShards, loss, defaultBypassLossThreshold)
}

func parityForLossThreshold(dataShards int, loss, bypassThreshold float64) int {
	if dataShards <= 0 || loss <= bypassThreshold {
		return 0
	}
	maxParity := min(4, max(1, dataShards/2))
	for parity := 1; parity <= maxParity; parity++ {
		if binomialTail(dataShards+parity, loss, parity) <= 0.005 {
			return parity
		}
	}
	return maxParity
}

// binomialTail returns P(X > successes) for X~Binomial(trials, probability).
func binomialTail(trials int, probability float64, successes int) float64 {
	if probability <= 0 {
		return 0
	}
	if probability >= 1 {
		return 1
	}
	term := math.Pow(1-probability, float64(trials))
	cumulative := term
	for outcomes := 1; outcomes <= successes; outcomes++ {
		term *= float64(trials-outcomes+1) / float64(outcomes)
		term *= probability / (1 - probability)
		cumulative += term
	}
	return max(0, min(1, 1-cumulative))
}

func MarshalFeedback(feedback Feedback) []byte {
	return marshalPacket(packet{
		kind:    KindFeedback,
		epoch:   feedback.Epoch,
		groupID: feedback.GroupID,
		index:   feedback.Missing,
		k:       feedback.Total,
		r:       feedback.Recovered,
	})
}
