//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

const (
	windowsManagementPipePath     = `\\.\pipe\wg-quic-management-v1`
	windowsManagementClientAccess = windows.FILE_GENERIC_READ |
		(windows.FILE_GENERIC_WRITE &^ windows.FILE_APPEND_DATA)

	// Authenticated users need read/write access so an unelevated half of a
	// split UAC token can submit a request. Authorization is performed from the
	// impersonation token after the request has been read. LocalSystem and full
	// Administrators retain full control, while the mandatory label rejects
	// writes from low-integrity processes.
	windowsManagementPipeSecuritySDDL = "O:SYG:SYD:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;AU)S:(ML;;NW;;;ME)"

	windowsTokenElevationTypeLimited = 3
)

var (
	errWindowsManagementUnauthorized = errors.New(
		"Windows management client is not authorized",
	)

	advapi32ImpersonateNamedPipeClient = windows.NewLazySystemDLL(
		"advapi32.dll",
	).NewProc("ImpersonateNamedPipeClient")
)

// newWindowsManagementPipeListener creates the fixed, local-only broker pipe.
// Its owner is explicitly LocalSystem so clients can reject a pipe squatter
// before sending a management request.
func newWindowsManagementPipeListener() (net.Listener, error) {
	descriptor, err := windowsManagementPipeSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	listener, err := (&namedpipe.ListenConfig{
		SecurityDescriptor: descriptor,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	}).Listen(windowsManagementPipePath)
	if err != nil {
		return nil, err
	}
	return listener, nil
}

func windowsManagementPipeSecurityDescriptor() (
	*windows.SECURITY_DESCRIPTOR,
	error,
) {
	descriptor, err := windows.SecurityDescriptorFromString(
		windowsManagementPipeSecuritySDDL,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"build Windows management pipe security descriptor: %w", err,
		)
	}
	return descriptor, nil
}

// dialWindowsManagementPipe connects with SECURITY_IDENTIFICATION, which lets
// the broker inspect the caller without granting it permission to act as the
// caller. ExpectedOwner prevents sending a request to a pipe not owned by
// LocalSystem. Native dial errors intentionally remain in the error chain so
// callers can distinguish a stopped broker from access denial.
func dialWindowsManagementPipe(ctx context.Context) (net.Conn, error) {
	config, err := windowsManagementPipeDialConfig()
	if err != nil {
		return nil, err
	}
	return config.DialContext(ctx, windowsManagementPipePath)
}

func windowsManagementPipeDialConfig() (*namedpipe.DialConfig, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	return &namedpipe.DialConfig{
		ExpectedOwner:      system,
		ImpersonationLevel: windows.SECURITY_IDENTIFICATION,
		DesiredAccess:      windowsManagementClientAccess,
	}, nil
}

// authorizeWindowsManagementPipeClient authorizes the identity attached to
// the last data read from connection. Windows associates named-pipe
// impersonation with the most recent server-side ReadFile, so the broker MUST
// call this only after at least one successful read (normally the bounded
// protocol preamble). Calling it before the first read can inspect the wrong
// client context.
func authorizeWindowsManagementPipeClient(connection net.Conn) error {
	pipe, ok := connection.(interface{ Handle() windows.Handle })
	if !ok {
		return errors.New("Windows management connection is not a named pipe")
	}
	token, err := windowsManagementPipeClientToken(pipe.Handle())
	if err != nil {
		return err
	}
	defer token.Close()

	identity, err := inspectWindowsManagementIdentity(token)
	if err != nil {
		return err
	}
	return authorizeWindowsManagementIdentity(identity)
}

type windowsManagementIdentity struct {
	localSystem                       bool
	administrator                     bool
	linkedAdministrator               bool
	limitedAdministratorDenyOnlyGroup bool
}

type windowsTokenGroupMembership struct {
	enabled       bool
	exactDenyOnly bool
}

func inspectWindowsManagementIdentity(
	token windows.Token,
) (windowsManagementIdentity, error) {
	var identity windowsManagementIdentity
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return identity, fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return identity, fmt.Errorf("inspect management client user: %w", err)
	}
	identity.localSystem = user.User.Sid.Equals(system)
	if identity.localSystem {
		return identity, nil
	}

	administrators, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return identity, fmt.Errorf("resolve Administrators SID: %w", err)
	}
	administratorMembership, err := windowsTokenGroupMembershipForSID(
		token, administrators,
	)
	if err != nil {
		return identity, fmt.Errorf(
			"inspect management client group membership: %w", err,
		)
	}
	identity.administrator = administratorMembership.enabled
	if identity.administrator {
		return identity, nil
	}

	elevationType, err := windowsTokenElevationType(token)
	if err != nil {
		return identity, fmt.Errorf(
			"inspect management client elevation type: %w", err,
		)
	}
	if elevationType != windowsTokenElevationTypeLimited {
		return identity, nil
	}
	identity.limitedAdministratorDenyOnlyGroup =
		administratorMembership.exactDenyOnly
	linked, err := token.GetLinkedToken()
	if err != nil {
		// CreateRestrictedToken(LUA_TOKEN) produces a genuine limited token
		// without necessarily attaching the full token as TokenLinkedToken. An
		// exact deny-only Administrators group is the kernel-produced marker that
		// the SID was filtered from the source token. Do not accept merely disabled
		// or partially matching group attributes.
		if identity.limitedAdministratorDenyOnlyGroup {
			return identity, nil
		}
		return identity, fmt.Errorf(
			"open management client's linked token: %w", err,
		)
	}
	defer linked.Close()
	linkedMembership, err := windowsTokenGroupMembershipForSID(
		linked, administrators,
	)
	if err != nil {
		return identity, fmt.Errorf(
			"inspect management client's linked Administrator membership: %w",
			err,
		)
	}
	identity.linkedAdministrator = linkedMembership.enabled
	return identity, nil
}

func authorizeWindowsManagementIdentity(
	identity windowsManagementIdentity,
) error {
	if identity.localSystem ||
		identity.administrator ||
		identity.linkedAdministrator ||
		identity.limitedAdministratorDenyOnlyGroup {
		return nil
	}
	return errWindowsManagementUnauthorized
}

func windowsTokenGroupMembershipForSID(
	token windows.Token,
	sid *windows.SID,
) (windowsTokenGroupMembership, error) {
	groups, err := token.GetTokenGroups()
	if err != nil {
		return windowsTokenGroupMembership{}, err
	}
	for _, group := range groups.AllGroups() {
		if !group.Sid.Equals(sid) {
			continue
		}
		return classifyWindowsTokenGroupAttributes(group.Attributes), nil
	}
	return windowsTokenGroupMembership{}, nil
}

func classifyWindowsTokenGroupAttributes(
	attributes uint32,
) windowsTokenGroupMembership {
	return windowsTokenGroupMembership{
		enabled: attributes&windows.SE_GROUP_ENABLED != 0 &&
			attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0,
		exactDenyOnly: attributes == windows.SE_GROUP_USE_FOR_DENY_ONLY,
	}
}

func windowsTokenElevationType(token windows.Token) (uint32, error) {
	var elevationType uint32
	var returned uint32
	err := windows.GetTokenInformation(
		token,
		windows.TokenElevationType,
		(*byte)(unsafe.Pointer(&elevationType)),
		uint32(unsafe.Sizeof(elevationType)),
		&returned,
	)
	if err != nil {
		return 0, err
	}
	if returned != uint32(unsafe.Sizeof(elevationType)) {
		return 0, windows.ERROR_BAD_LENGTH
	}
	return elevationType, nil
}

type windowsManagementTokenResult struct {
	token windows.Token
	err   error
}

func windowsManagementPipeClientToken(
	pipe windows.Handle,
) (windows.Token, error) {
	result := make(chan windowsManagementTokenResult, 1)
	go func() {
		// Impersonation is thread-local. A dedicated locked thread ensures Go
		// cannot migrate this operation, and lets us discard the thread rather
		// than return it to the runtime if RevertToSelf ever fails.
		runtime.LockOSThread()
		if err := impersonateWindowsNamedPipeClient(pipe); err != nil {
			runtime.UnlockOSThread()
			result <- windowsManagementTokenResult{err: fmt.Errorf(
				"identify Windows management pipe client: %w", err,
			)}
			return
		}

		var token windows.Token
		openErr := windows.OpenThreadToken(
			windows.CurrentThread(), windows.TOKEN_QUERY, true, &token,
		)
		revertErr := windows.RevertToSelf()
		if revertErr != nil {
			if token != 0 {
				_ = token.Close()
			}
			result <- windowsManagementTokenResult{err: fmt.Errorf(
				"revert Windows management client impersonation: %w",
				revertErr,
			)}
			// Do not unlock a thread that may still carry the client's token.
			// Go terminates a locked OS thread when its goroutine exits.
			runtime.Goexit()
		}
		runtime.UnlockOSThread()
		if openErr != nil {
			result <- windowsManagementTokenResult{err: fmt.Errorf(
				"open Windows management client token: %w", openErr,
			)}
			return
		}
		result <- windowsManagementTokenResult{token: token}
	}()
	captured := <-result
	return captured.token, captured.err
}

func impersonateWindowsNamedPipeClient(pipe windows.Handle) error {
	result, _, callErr := advapi32ImpersonateNamedPipeClient.Call(
		uintptr(pipe),
	)
	if result != 0 {
		return nil
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
		return errno
	}
	return windows.ERROR_GEN_FAILURE
}
