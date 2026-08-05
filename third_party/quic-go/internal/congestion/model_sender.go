package congestion

import (
	"time"

	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/qlogwriter"
)

const (
	modelStateStartup uint64 = iota
	modelStateProbe
)

const (
	modelMinCongestionWindowPackets = 4
	modelBandwidthWindow            = 10
	modelStartupPacingGain          = 2.0
	modelProbePacingGain            = 1.10
	modelCwndGain                   = 2.0
	modelQueueThreshold             = 1.25
	modelSevereQueueThreshold       = 1.50
	modelPathRTTDecreaseConfirm     = 50 * time.Millisecond
	modelPathRTTIncreaseConfirm     = 100 * time.Millisecond
)

// modelSender is a compact delivery-model controller for the datagram carrier.
//
// It intentionally uses loss only together with queue growth or ECN. FEC
// outcomes classify path loss and drive protection, but random residual loss
// alone is not proof of congestion. This avoids a Reno-style multiplicative
// collapse while preserving one budget for data and parity packets.
type modelSender struct {
	clock     Clock
	rttStats  *utils.RTTStats
	connStats *utils.ConnectionStats
	pacer     *pacer

	maxDatagramSize   protocol.ByteCount
	congestionWindow  protocol.ByteCount
	bandwidthEstimate Bandwidth
	state             uint64

	largestSent         protocol.PacketNumber
	roundEnd            protocol.PacketNumber
	fullBandwidth       Bandwidth
	fullBandwidthRounds int

	ackWindowActive      bool
	ackWindowStart       monotime.Time
	ackWindowBytes       protocol.ByteCount
	bandwidthSamples     [modelBandwidthWindow]Bandwidth
	bandwidthSampleIndex int
	bandwidthSampleCount int

	lastQueueResponse monotime.Time
	rttSeeded         bool

	propagationRTT     time.Duration
	lowerRTTCandidate  time.Duration
	lowerRTTSince      monotime.Time
	higherRTTCandidate time.Duration
	higherRTTSince     monotime.Time
	queueSignalResume  monotime.Time

	lastFECTotal     uint64
	lastFECMissing   uint64
	lastFECRecovered uint64
	fecSignalReady   bool
	fecRecoverable   float64
	fecResidual      float64
}

var (
	_ SendAlgorithm               = &modelSender{}
	_ SendAlgorithmWithDebugInfos = &modelSender{}
	_ SendAlgorithmWithStats      = &modelSender{}
)

func NewModelSender(
	clock Clock,
	rttStats *utils.RTTStats,
	connStats *utils.ConnectionStats,
	initialMaxDatagramSize protocol.ByteCount,
	_ qlogwriter.Recorder,
) *modelSender {
	initialWindow := initialCongestionWindow * initialMaxDatagramSize
	rtt := rttStats.SmoothedRTT()
	if rtt <= 0 {
		rtt = utils.DefaultInitialRTT
	}
	m := &modelSender{
		clock:             clock,
		rttStats:          rttStats,
		connStats:         connStats,
		maxDatagramSize:   initialMaxDatagramSize,
		congestionWindow:  initialWindow,
		bandwidthEstimate: BandwidthFromDelta(initialWindow, rtt),
		state:             modelStateStartup,
		largestSent:       protocol.InvalidPacketNumber,
		roundEnd:          protocol.InvalidPacketNumber,
		rttSeeded:         rttStats.HasMeasurement(),
	}
	if rttStats.HasMeasurement() {
		m.propagationRTT = rttStats.MinRTT()
	}
	m.pacer = newPacer(m.pacerBandwidth)
	return m
}

func (m *modelSender) TimeUntilSend(protocol.ByteCount) monotime.Time {
	return m.pacer.TimeUntilSend()
}

func (m *modelSender) HasPacingBudget(now monotime.Time) bool {
	return m.pacer.Budget(now) >= m.maxDatagramSize
}

func (m *modelSender) OnPacketSent(
	sentTime monotime.Time,
	_ protocol.ByteCount,
	packetNumber protocol.PacketNumber,
	bytes protocol.ByteCount,
	isRetransmittable bool,
) {
	m.pacer.SentPacket(sentTime, bytes)
	if isRetransmittable {
		m.largestSent = max(m.largestSent, packetNumber)
		if m.roundEnd == protocol.InvalidPacketNumber {
			m.roundEnd = m.largestSent
		}
	}
}

func (m *modelSender) CanSend(bytesInFlight protocol.ByteCount) bool {
	return bytesInFlight < m.congestionWindow
}

func (m *modelSender) MaybeExitSlowStart() {}

func (m *modelSender) OnPacketAcked(
	packetNumber protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime monotime.Time,
) {
	m.observePathRTT(eventTime)
	m.seedFromFirstRTT()
	m.updateFECSignal()
	m.observeDelivery(ackedBytes, priorInFlight, eventTime)
	m.updateRound(packetNumber, priorInFlight >= m.congestionWindow/2)
	m.respondToPersistentQueue(eventTime)
	m.updateCongestionWindow(ackedBytes, priorInFlight)
}

func (m *modelSender) OnCongestionEvent(
	_ protocol.PacketNumber,
	lostBytes protocol.ByteCount,
	_ protocol.ByteCount,
) {
	if lostBytes > 0 {
		m.connStats.PacketsLost.Add(1)
		m.connStats.BytesLost.Add(uint64(lostBytes))
	}
	m.updateFECSignal()

	// A zero-byte event is ECN. Treat it as an explicit congestion signal.
	if lostBytes == 0 {
		m.reduceModel(0.75)
		return
	}
	if m.hasStandingQueue(m.clock.Now(), modelQueueThreshold) {
		m.reduceModel(0.85)
	}
}

func (m *modelSender) OnRetransmissionTimeout(packetsRetransmitted bool) {
	if !packetsRetransmitted {
		return
	}
	m.congestionWindow = m.minCongestionWindow()
	rtt := m.modelRTT()
	m.bandwidthEstimate = BandwidthFromDelta(m.congestionWindow, rtt)
	m.clearBandwidthWindow()
	m.state = modelStateStartup
	m.fullBandwidth = 0
	m.fullBandwidthRounds = 0
}

func (m *modelSender) SetMaxDatagramSize(size protocol.ByteCount) {
	wasMinimum := m.congestionWindow == m.minCongestionWindow()
	m.maxDatagramSize = size
	if wasMinimum {
		m.congestionWindow = m.minCongestionWindow()
	}
	m.pacer.SetMaxDatagramSize(size)
}

func (m *modelSender) InSlowStart() bool {
	return m.state == modelStateStartup
}

func (m *modelSender) InRecovery() bool {
	return false
}

func (m *modelSender) GetCongestionWindow() protocol.ByteCount {
	return m.congestionWindow
}

func (m *modelSender) Stats() SenderStats {
	queueDelay := m.rttStats.SmoothedRTT() - m.modelRTT()
	if queueDelay < 0 {
		queueDelay = 0
	}
	return SenderStats{
		CongestionWindow:      m.congestionWindow,
		BandwidthEstimate:     m.bandwidthEstimate,
		PacingRate:            m.pacingRate(),
		PropagationRTT:        m.modelRTT(),
		QueueDelay:            queueDelay,
		FECRecoverableLossPPM: ratePPM(m.fecRecoverable),
		FECResidualLossPPM:    ratePPM(m.fecResidual),
		ModelState:            m.state,
	}
}

func (m *modelSender) minCongestionWindow() protocol.ByteCount {
	return modelMinCongestionWindowPackets * m.maxDatagramSize
}

func (m *modelSender) maxCongestionWindow() protocol.ByteCount {
	return protocol.MaxCongestionWindowPackets * m.maxDatagramSize
}

func (m *modelSender) modelRTT() time.Duration {
	rtt := m.propagationRTT
	if rtt <= 0 {
		rtt = m.rttStats.MinRTT()
	}
	if rtt <= 0 {
		rtt = m.rttStats.SmoothedRTT()
	}
	if rtt <= 0 {
		rtt = utils.DefaultInitialRTT
	}
	return rtt
}

func (m *modelSender) hasStandingQueue(now monotime.Time, ratio float64) bool {
	if !m.queueSignalResume.IsZero() && now < m.queueSignalResume {
		return false
	}
	minRTT := m.modelRTT()
	smoothedRTT := m.rttStats.SmoothedRTT()
	if minRTT <= 0 || smoothedRTT <= minRTT {
		return false
	}
	relativeThreshold := time.Duration((ratio - 1) * float64(minRTT))
	// Sub-millisecond paths routinely see scheduler and ACK batching noise
	// that is large as a ratio but too small to represent a harmful queue.
	return smoothedRTT-minRTT >= max(5*time.Millisecond, relativeThreshold)
}

// observePathRTT maintains a path-local propagation RTT instead of relying on
// QUIC's connection-lifetime minimum. A sustained latency increase is accepted
// after the controller has drained to a small window; a very large decrease
// must persist long enough to reject one-off samples during route / qdisc
// transitions.
func (m *modelSender) observePathRTT(now monotime.Time) {
	sample := m.rttStats.LatestRTT()
	if sample <= 0 {
		return
	}
	if m.propagationRTT <= 0 {
		m.propagationRTT = sample
		return
	}

	if sample < m.propagationRTT {
		m.higherRTTSince = 0
		m.higherRTTCandidate = 0
		// Ordinary new minima are safe to use immediately. Dramatic drops are
		// confirmed, since a link reconfiguration can briefly bypass shaping.
		if sample*4 >= m.propagationRTT {
			m.acceptLowerPathRTT(now, sample)
			return
		}
		if m.lowerRTTSince.IsZero() ||
			sample > 2*m.lowerRTTCandidate ||
			m.lowerRTTCandidate > 2*sample {
			m.lowerRTTSince = now
			m.lowerRTTCandidate = sample
			return
		}
		m.lowerRTTCandidate = min(m.lowerRTTCandidate, sample)
		if now.Sub(m.lowerRTTSince) >= modelPathRTTDecreaseConfirm {
			m.acceptLowerPathRTT(now, m.lowerRTTCandidate)
		}
		return
	}
	m.lowerRTTSince = 0
	m.lowerRTTCandidate = 0

	// Do not relabel an ordinary growing queue as propagation delay. Once the
	// controller has drained to two minimum windows and the high RTT persists,
	// the old baseline is no longer actionable and likely belongs to an old
	// access path.
	if sample*4 <= m.propagationRTT*5 ||
		m.congestionWindow > 2*m.minCongestionWindow() {
		m.higherRTTSince = 0
		m.higherRTTCandidate = 0
		return
	}
	if m.higherRTTSince.IsZero() {
		m.higherRTTSince = now
		m.higherRTTCandidate = sample
		return
	}
	m.higherRTTCandidate = min(m.higherRTTCandidate, sample)
	confirm := max(modelPathRTTIncreaseConfirm, 2*m.propagationRTT)
	if now.Sub(m.higherRTTSince) >= confirm {
		m.propagationRTT = m.higherRTTCandidate
		m.higherRTTSince = 0
		m.higherRTTCandidate = 0
	}
}

func (m *modelSender) acceptLowerPathRTT(now monotime.Time, sample time.Duration) {
	previous := m.propagationRTT
	m.propagationRTT = sample
	m.lowerRTTSince = 0
	m.lowerRTTCandidate = 0
	// The connection-wide smoothed RTT still contains samples from the old
	// path. Avoid treating that filter lag as a new standing queue.
	m.queueSignalResume = now.Add(max(50*time.Millisecond, 2*previous))
}

func (m *modelSender) pacingGain() float64 {
	if m.state == modelStateStartup {
		return modelStartupPacingGain
	}
	return modelProbePacingGain
}

func (m *modelSender) pacingRate() Bandwidth {
	rate := Bandwidth(float64(m.bandwidthEstimate) * m.pacingGain())
	if rate == 0 {
		return BitsPerSecond
	}
	return rate
}

func (m *modelSender) pacerBandwidth() Bandwidth {
	// pacer adds its own 1.25 scheduling headroom. Cancel that adjustment so
	// the externally reported model pacing rate is the actual wire target.
	return m.pacingRate() * 4 / 5
}

func (m *modelSender) deliverySamplePeriod() time.Duration {
	period := m.rttStats.SmoothedRTT() / 8
	return min(50*time.Millisecond, max(5*time.Millisecond, period))
}

func (m *modelSender) observeDelivery(
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime monotime.Time,
) {
	if !m.ackWindowActive {
		m.ackWindowActive = true
		m.ackWindowStart = eventTime
		m.ackWindowBytes = ackedBytes
		return
	}
	m.ackWindowBytes += ackedBytes
	elapsed := eventTime.Sub(m.ackWindowStart)
	if elapsed < m.deliverySamplePeriod() {
		return
	}
	if elapsed <= 0 {
		m.ackWindowStart = eventTime
		m.ackWindowBytes = 0
		return
	}
	sample := BandwidthFromDelta(m.ackWindowBytes, elapsed)
	// ACK compression cannot create capacity. Bound the sample by the flight
	// delivered over one minimum RTT, with modest aggregation headroom.
	if priorInFlight > 0 {
		flightRate := BandwidthFromDelta(priorInFlight, m.modelRTT())
		sample = min(sample, flightRate*3/2)
	}
	// Application-limited samples must not lower the delivery model, but a
	// faster sample is still evidence that the path can deliver at least that
	// rate. The flight/RTT bound above prevents a compressed ACK burst from
	// manufacturing capacity.
	if priorInFlight >= m.congestionWindow/2 || sample > m.bandwidthEstimate {
		m.recordBandwidthSample(sample)
	}
	m.ackWindowStart = eventTime
	m.ackWindowBytes = 0
}

func (m *modelSender) recordBandwidthSample(sample Bandwidth) {
	if sample == 0 {
		return
	}
	m.bandwidthSamples[m.bandwidthSampleIndex] = sample
	m.bandwidthSampleIndex = (m.bandwidthSampleIndex + 1) % len(m.bandwidthSamples)
	m.bandwidthSampleCount = min(m.bandwidthSampleCount+1, len(m.bandwidthSamples))
	estimate := m.bandwidthEstimate
	for i := 0; i < m.bandwidthSampleCount; i++ {
		estimate = max(estimate, m.bandwidthSamples[i])
	}
	m.bandwidthEstimate = estimate
}

func (m *modelSender) updateRound(acked protocol.PacketNumber, capacityLimited bool) {
	if m.roundEnd == protocol.InvalidPacketNumber || acked <= m.roundEnd {
		return
	}
	m.roundEnd = m.largestSent
	if m.state != modelStateStartup || !capacityLimited {
		return
	}
	if m.fullBandwidth == 0 || m.bandwidthEstimate >= m.fullBandwidth*5/4 {
		m.fullBandwidth = m.bandwidthEstimate
		m.fullBandwidthRounds = 0
		return
	}
	m.fullBandwidthRounds++
	if m.fullBandwidthRounds >= 3 {
		m.state = modelStateProbe
	}
}

func (m *modelSender) respondToPersistentQueue(now monotime.Time) {
	if !m.hasStandingQueue(now, modelSevereQueueThreshold) {
		return
	}
	interval := m.modelRTT()
	if !m.lastQueueResponse.IsZero() && now.Sub(m.lastQueueResponse) < interval {
		return
	}
	m.lastQueueResponse = now
	m.reduceModel(0.90)
}

func (m *modelSender) seedFromFirstRTT() {
	if m.rttSeeded || !m.rttStats.HasMeasurement() {
		return
	}
	m.rttSeeded = true
	seed := BandwidthFromDelta(m.congestionWindow, m.modelRTT())
	if seed > m.bandwidthEstimate {
		m.bandwidthEstimate = seed
		m.clearBandwidthWindow()
	}
}

func (m *modelSender) updateCongestionWindow(
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
) {
	if m.state == modelStateStartup {
		if priorInFlight >= m.congestionWindow/2 {
			m.congestionWindow = min(m.maxCongestionWindow(), m.congestionWindow+ackedBytes)
		}
		return
	}
	target := m.targetCongestionWindow()
	switch {
	case m.congestionWindow < target && priorInFlight >= m.congestionWindow/2:
		m.congestionWindow = min(target, m.congestionWindow+ackedBytes)
	case m.congestionWindow > target:
		m.congestionWindow -= min(ackedBytes, m.congestionWindow-target)
	}
	m.congestionWindow = max(m.minCongestionWindow(), m.congestionWindow)
}

func (m *modelSender) targetCongestionWindow() protocol.ByteCount {
	bytesPerSecond := uint64(m.bandwidthEstimate / BytesPerSecond)
	bdp := protocol.ByteCount(bytesPerSecond * uint64(m.modelRTT()) / uint64(time.Second))
	target := protocol.ByteCount(float64(bdp) * modelCwndGain)
	return min(m.maxCongestionWindow(), max(m.minCongestionWindow(), target))
}

func (m *modelSender) reduceModel(factor float64) {
	reduced := Bandwidth(float64(m.bandwidthEstimate) * factor)
	minimum := BandwidthFromDelta(m.minCongestionWindow(), m.modelRTT())
	m.bandwidthEstimate = max(minimum, reduced)
	m.clearBandwidthWindow()
	m.congestionWindow = min(m.congestionWindow, m.targetCongestionWindow())
	m.congestionWindow = max(m.minCongestionWindow(), m.congestionWindow)
	m.state = modelStateProbe
}

func (m *modelSender) clearBandwidthWindow() {
	clear(m.bandwidthSamples[:])
	m.bandwidthSampleIndex = 0
	m.bandwidthSampleCount = 0
}

func (m *modelSender) updateFECSignal() {
	total := m.connStats.FECObservedData.Load()
	missing := m.connStats.FECObservedMissing.Load()
	recovered := m.connStats.FECObservedRecovered.Load()
	if total < m.lastFECTotal || missing < m.lastFECMissing || recovered < m.lastFECRecovered {
		m.lastFECTotal, m.lastFECMissing, m.lastFECRecovered = total, missing, recovered
		m.fecSignalReady = false
		return
	}
	deltaTotal := total - m.lastFECTotal
	if deltaTotal == 0 {
		return
	}
	deltaMissing := min(missing-m.lastFECMissing, deltaTotal)
	deltaRecovered := min(recovered-m.lastFECRecovered, deltaMissing)
	recoverable := float64(deltaRecovered) / float64(deltaTotal)
	residual := float64(deltaMissing-deltaRecovered) / float64(deltaTotal)
	alpha := min(0.5, max(0.125, float64(deltaTotal)/64))
	if !m.fecSignalReady {
		m.fecRecoverable = recoverable
		m.fecResidual = residual
		m.fecSignalReady = true
	} else {
		m.fecRecoverable = (1-alpha)*m.fecRecoverable + alpha*recoverable
		m.fecResidual = (1-alpha)*m.fecResidual + alpha*residual
	}
	m.lastFECTotal, m.lastFECMissing, m.lastFECRecovered = total, missing, recovered
}

func ratePPM(rate float64) uint64 {
	if rate <= 0 {
		return 0
	}
	return uint64(min(1.0, rate)*1_000_000 + 0.5)
}
