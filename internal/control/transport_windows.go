//go:build windows

package control

import (
	"net"
	"time"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

// Let Windows assign the pipe owner to the creating process. Forcing
// LocalSystem as owner makes elevated foreground diagnostics fail even though
// the DACL correctly grants access only to LocalSystem and Administrators.
const controlPipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

func listen(path string) (net.Listener, func() error, error) {
	descriptor, err := windows.SecurityDescriptorFromString(controlPipeSecurityDescriptor)
	if err != nil {
		return nil, nil, err
	}
	listener, err := (&namedpipe.ListenConfig{
		SecurityDescriptor: descriptor,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	}).Listen(path)
	if err != nil {
		return nil, nil, err
	}
	return listener, nil, nil
}

func dial(path string, timeout time.Duration) (net.Conn, error) {
	return namedpipe.DialTimeout(path, timeout)
}
