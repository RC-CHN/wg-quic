//go:build !linux && !freebsd

package armorbind

import "net"

func setSocketMark(net.PacketConn, uint32) error {
	return nil
}
