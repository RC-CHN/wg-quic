//go:build linux

package quic

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

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
		socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
	}); err != nil {
		return err
	}
	return socketErr
}
