//go:build linux

package quic

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stretchr/testify/require"
)

type failedGSOProbe struct {
	called bool
	err    error
}

func (c *failedGSOProbe) Control(f func(uintptr)) error {
	c.called = true
	if c.err == nil {
		f(^uintptr(0)) // invalid descriptor: the socket option query must fail
	}
	return c.err
}

func (*failedGSOProbe) Read(func(uintptr) bool) error  { panic("unexpected Read") }
func (*failedGSOProbe) Write(func(uintptr) bool) error { panic("unexpected Write") }

var _ syscall.RawConn = (*failedGSOProbe)(nil)

func TestGSOProbeHonorsDisableAndFailures(t *testing.T) {
	t.Setenv("QUIC_GO_DISABLE_GSO", "true")
	probe := &failedGSOProbe{}
	require.False(t, isGSOEnabled(probe))
	require.False(t, probe.called)
	t.Setenv("QUIC_GO_DISABLE_GSO", "false")
	require.False(t, isGSOEnabled(&failedGSOProbe{err: net.ErrClosed}))
	require.False(t, isGSOEnabled(&failedGSOProbe{}))
}

func TestGSOProbeUsesKernelCapability(t *testing.T) {
	t.Setenv("QUIC_GO_DISABLE_GSO", "false")
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer c.Close()
	raw, err := c.SyscallConn()
	require.NoError(t, err)
	var supported bool
	require.NoError(t, raw.Control(func(fd uintptr) {
		_, err := unix.GetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_SEGMENT)
		supported = err == nil
	}))
	require.Equal(t, supported && kernelVersionMajor >= 5, isGSOEnabled(raw))
}

var (
	errGSO          = &os.SyscallError{Err: unix.EIO}
	errNotPermitted = &os.SyscallError{Syscall: "sendmsg", Err: unix.EPERM}
)

func TestForcingReceiveBufferSize(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Must be root to force change the receive buffer size")
	}

	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer c.Close()
	syscallConn, err := c.(*net.UDPConn).SyscallConn()
	require.NoError(t, err)

	const small = 256 << 10 // 256 KB
	require.NoError(t, forceSetReceiveBuffer(syscallConn, small))

	size, err := inspectReadBuffer(syscallConn)
	require.NoError(t, err)
	// the kernel doubles this value (to allow space for bookkeeping overhead)
	require.Equal(t, 2*small, size)

	const large = 32 << 20 // 32 MB
	require.NoError(t, forceSetReceiveBuffer(syscallConn, large))
	size, err = inspectReadBuffer(syscallConn)
	require.NoError(t, err)
	// the kernel doubles this value (to allow space for bookkeeping overhead)
	require.Equal(t, 2*large, size)
}

func TestForcingSendBufferSize(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Must be root to force change the send buffer size")
	}

	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer c.Close()
	syscallConn, err := c.(*net.UDPConn).SyscallConn()
	require.NoError(t, err)

	const small = 256 << 10 // 256 KB
	require.NoError(t, forceSetSendBuffer(syscallConn, small))

	size, err := inspectWriteBuffer(syscallConn)
	require.NoError(t, err)
	// the kernel doubles this value (to allow space for bookkeeping overhead)
	require.Equal(t, 2*small, size)

	const large = 32 << 20 // 32 MB
	require.NoError(t, forceSetSendBuffer(syscallConn, large))
	size, err = inspectWriteBuffer(syscallConn)
	require.NoError(t, err)
	// the kernel doubles this value (to allow space for bookkeeping overhead)
	require.Equal(t, 2*large, size)
}

func TestGSOError(t *testing.T) {
	require.True(t, isGSOError(errGSO))
	require.False(t, isGSOError(nil))
	require.False(t, isGSOError(errors.New("test")))
}
