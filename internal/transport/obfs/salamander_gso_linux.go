//go:build linux

package obfs

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func growGSOSegmentSize(oob []byte, overhead int) (uint16, error) {
	for len(oob) > 0 {
		header, body, remainder, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			return 0, err
		}
		if header.Level == unix.IPPROTO_UDP && header.Type == unix.UDP_SEGMENT {
			if len(body) != 2 {
				return 0, errors.New("invalid UDP_SEGMENT control message")
			}
			size := binary.NativeEndian.Uint16(body)
			if int(size)+overhead > int(^uint16(0)) {
				return 0, errors.New("Salamander GSO segment size overflow")
			}
			binary.NativeEndian.PutUint16(body, size+uint16(overhead))
			return size, nil
		}
		oob = remainder
	}
	return 0, nil
}
