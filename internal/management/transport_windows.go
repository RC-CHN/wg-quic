//go:build windows

package management

import (
	"context"
	"fmt"
	"net"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

const managementPipeSecurityDescriptor = "O:SYG:SYD:P(A;;GA;;;SY)(A;;GA;;;BA)"

func listen(path string) (net.Listener, func() error, error) {
	descriptor, err := windows.SecurityDescriptorFromString(managementPipeSecurityDescriptor)
	if err != nil {
		return nil, nil, fmt.Errorf("build management pipe security descriptor: %w", err)
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

func dial(ctx context.Context, path string) (net.Conn, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	const access = windows.FILE_GENERIC_READ |
		(windows.FILE_GENERIC_WRITE &^ windows.FILE_APPEND_DATA)
	return (&namedpipe.DialConfig{
		ExpectedOwner:      system,
		ImpersonationLevel: windows.SECURITY_IDENTIFICATION,
		DesiredAccess:      access,
	}).DialContext(ctx, path)
}
