package fec

import "sync"

type Feedback struct {
	Epoch     uint16
	GroupID   uint64
	Missing   uint16
	Recovered uint16
	Total     uint16
}

type Controller struct {
	mu sync.Mutex

	parity          int
	zeroLossGroups  int
	lossWindowTotal int
	lossWindowLost  int
}

func NewController() *Controller {
	return &Controller{parity: 1}
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
	previous := c.parity
	c.lossWindowTotal += int(feedback.Total)
	c.lossWindowLost += int(feedback.Missing)
	if feedback.Missing == 0 {
		c.zeroLossGroups++
	} else {
		c.zeroLossGroups = 0
	}

	if feedback.Missing > feedback.Recovered {
		c.parity = min(c.parity+1, 4)
		c.lossWindowTotal, c.lossWindowLost = 0, 0
		return c.parity != previous
	}
	if c.lossWindowTotal >= 32 {
		loss := float64(c.lossWindowLost) / float64(c.lossWindowTotal)
		switch {
		case loss >= 0.15:
			c.parity = min(c.parity+2, 4)
		case loss >= 0.02:
			c.parity = min(c.parity+1, 4)
		}
		c.lossWindowTotal, c.lossWindowLost = 0, 0
	}
	if c.zeroLossGroups >= 32 && c.parity > 0 {
		c.parity--
		c.zeroLossGroups = 0
	}
	return c.parity != previous
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
