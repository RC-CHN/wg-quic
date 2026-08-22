//go:build windows

package management

import (
	"context"
	"fmt"
	"net"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

const managementPipeDACL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

func listen(path string) (net.Listener, func() error, error) {
	descriptor, err := managementPipeSecurityDescriptor()
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

// managementPipeSecurityDescriptor pins ownership to the creator rather than
// trying to assign LocalSystem unconditionally. Installed tunnel supervisors
// run as LocalSystem, so their owner remains LocalSystem. An explicitly
// elevated same-user debug process can also create and test the transport
// without requiring SeRestorePrivilege to assign somebody else's SID.
func managementPipeSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve management pipe creator SID: %w", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + user.User.Sid.String() + managementPipeDACL,
	)
	if err != nil {
		return nil, fmt.Errorf("build management pipe security descriptor: %w", err)
	}
	return descriptor, nil
}

func dial(ctx context.Context, path string) (net.Conn, error) {
	owners, err := managementPipeExpectedOwners()
	if err != nil {
		return nil, err
	}
	const access = windows.FILE_GENERIC_READ |
		(windows.FILE_GENERIC_WRITE &^ windows.FILE_APPEND_DATA)
	return (&namedpipe.DialConfig{
		ExpectedOwners:     owners,
		ImpersonationLevel: windows.SECURITY_IDENTIFICATION,
		DesiredAccess:      access,
	}).DialContext(ctx, path)
}

func managementPipeExpectedOwners() ([]*windows.SID, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	owners := []*windows.SID{system}
	administrators, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve Administrators SID: %w", err)
	}
	token := windows.GetCurrentProcessToken()
	elevated, err := token.IsMember(administrators)
	if err != nil {
		return nil, fmt.Errorf("inspect management client Administrator membership: %w", err)
	}
	if !elevated {
		return owners, nil
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve elevated management client SID: %w", err)
	}
	if user.User.Sid.Equals(system) {
		return owners, nil
	}
	owner, err := user.User.Sid.Copy()
	if err != nil {
		return nil, fmt.Errorf("copy elevated management client SID: %w", err)
	}
	return append(owners, owner), nil
}
