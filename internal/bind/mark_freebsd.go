//go:build freebsd

package armorbind

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// FreeBSD exposes WireGuard's fwmark-equivalent metadata as SO_USER_COOKIE.
// This matches upstream wireguard-go's StdNetBind behavior. Routing around a
// full-tunnel default still uses explicit endpoint routes.
const soUserCookie = 0x1015

func setSocketMark(connection net.PacketConn, mark uint32) error {
	syscallConnection, ok := connection.(syscall.Conn)
	if !ok {
		return fmt.Errorf("packet connection %T does not expose a syscall connection", connection)
	}
	rawConnection, err := syscallConnection.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	if err := rawConnection.Control(func(fd uintptr) {
		socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, soUserCookie, int(mark))
	}); err != nil {
		return err
	}
	return socketErr
}
