package quic

// Keep at most 5 ms of estimated service in the packet packer's queue. A
// four-packet floor covers scheduler jitter at low rates; the existing packet
// cap still bounds memory at high rates. Data and FEC parity share this budget.
// Zero estimates (e.g. controllers without bandwidth statistics) retain the
// ordinary bounded queue until a usable pacing estimate becomes available.
func datagramSendBudget(bandwidth, pacing uint64) int {
	rate := bandwidth
	if rate == 0 || (pacing != 0 && pacing < rate) {
		rate = pacing
	}
	const maximum = maxDatagramSendQueueLen * DatagramSendBufferSize
	if rate == 0 {
		return maximum
	}
	return int(min(uint64(maximum), max(uint64(4*1200), rate/1600)))
}
