//go:build windows

package control

import (
	"errors"
	"net"
	"os"
	"time"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

// Let Windows assign the pipe owner to the creating process. Forcing
// LocalSystem as owner makes elevated foreground diagnostics fail even though
// the DACL correctly grants access only to LocalSystem and Administrators.
const controlPipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

// The public endpoint is status-only at the protocol layer. FILE_PIPE_REJECT_REMOTE_CLIENTS
// also prevents SMB clients from reaching it. Local users need
// duplex access because a request and response share one named pipe handle.
const readOnlyStatusPipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;BU)"

func listen(path string) (net.Listener, func() error, error) {
	return listenWithSecurityDescriptor(path, controlPipeSecurityDescriptor)
}

func listenReadOnlyStatus(
	path string,
) (net.Listener, func() error, error) {
	return listenWithSecurityDescriptor(
		readOnlyStatusPath(path),
		readOnlyStatusPipeSecurityDescriptor,
	)
}

func listenWithSecurityDescriptor(
	path string,
	sddl string,
) (net.Listener, func() error, error) {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
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
	connection, err := namedpipe.DialTimeout(path, timeout)
	if err != nil {
		return nil, normalizeNamedPipeDialError(err)
	}
	return connection, nil
}

// Go's portable os.ErrNotExist classification is not consistent across all
// Windows versions for native named-pipe errors. Normalize the two native
// missing-path errors at the transport boundary while preserving the original
// error text for diagnostics. In particular, do not collapse access-denied or
// busy-pipe failures into the normal "interface is inactive" state.
type missingNamedPipeError struct {
	err error
}

func (err *missingNamedPipeError) Error() string {
	return err.err.Error()
}

func (err *missingNamedPipeError) Unwrap() error {
	return err.err
}

func (err *missingNamedPipeError) Is(target error) bool {
	return target == os.ErrNotExist || errors.Is(err.err, target)
}

func normalizeNamedPipeDialError(err error) error {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return &missingNamedPipeError{err: err}
	}
	return err
}

func readOnlyStatusPath(path string) string {
	return path + "-status"
}
