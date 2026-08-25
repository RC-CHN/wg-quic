//go:build !linux

package quic

func enableReceiveQueueOverflow(int) bool { return false }

func parseReceiveQueueOverflow(int32, int32, []byte) (uint32, bool) {
	return 0, false
}
