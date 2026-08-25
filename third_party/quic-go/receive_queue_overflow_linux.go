//go:build linux

package quic

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

func enableReceiveQueueOverflow(fd int) bool {
	return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1) == nil
}

func parseReceiveQueueOverflow(level, messageType int32, body []byte) (uint32, bool) {
	if level != unix.SOL_SOCKET || messageType != unix.SO_RXQ_OVFL || len(body) != 4 {
		return 0, false
	}
	return binary.NativeEndian.Uint32(body), true
}
