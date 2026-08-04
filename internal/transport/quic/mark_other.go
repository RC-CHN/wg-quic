//go:build !linux && !freebsd

package quic

import "net"

func setSocketMark(net.PacketConn, uint32) error {
	return nil
}
