//go:build windows

// limited-token-launcher creates the same kernel-defined LUA token shape used
// by a UAC-filtered Administrator and starts a command with that token in the
// launcher's current session. It exists only as a Windows integration-test
// fixture; production privilege transitions remain owned by wg-quic.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createRestrictedTokenLUA        = 0x00000004
	tokenElevationTypeLimited       = 3
	limitedTokenLauncherUsage       = "limited-token-launcher -- command [argument ...]"
	interactiveWindowStationDesktop = `winsta0\default`
)

var createRestrictedToken = windows.NewLazySystemDLL(
	"advapi32.dll",
).NewProc("CreateRestrictedToken")

func main() {
	command, err := parseCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, limitedTokenLauncherUsage)
		fmt.Fprintln(os.Stderr, "limited-token-launcher:", err)
		os.Exit(2)
	}
	exitCode, sessionID, err := runLimitedCommand(command)
	if err != nil {
		fmt.Fprintln(os.Stderr, "limited-token-launcher:", err)
		os.Exit(1)
	}
	fmt.Fprintf(
		os.Stderr,
		"limited-token-launcher: child completed in session %d with exit code %d\n",
		sessionID,
		exitCode,
	)
	os.Exit(int(exitCode))
}

func parseCommand(arguments []string) ([]string, error) {
	if len(arguments) > 0 && arguments[0] == "--" {
		arguments = arguments[1:]
	}
	if len(arguments) == 0 || arguments[0] == "" {
		return nil, errors.New("a command is required")
	}
	return append([]string(nil), arguments...), nil
}

func runLimitedCommand(command []string) (uint32, uint32, error) {
	source, err := openLauncherSourceToken()
	if err != nil {
		return 0, 0, err
	}
	defer source.Close()

	enabled, exactDenyOnly, err := administratorGroupState(source)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect launcher source token: %w", err)
	}
	if !enabled || exactDenyOnly {
		return 0, 0, errors.New(
			"launcher requires a full Administrator source token",
		)
	}

	limited, err := createLUARestrictedToken(source)
	if err != nil {
		return 0, 0, err
	}
	defer limited.Close()
	sessionID, err := verifyLUARestrictedToken(limited)
	if err != nil {
		return 0, 0, err
	}

	exitCode, err := launchCommandAsUser(limited, command)
	if err != nil {
		return 0, sessionID, err
	}
	return exitCode, sessionID, nil
}

func openLauncherSourceToken() (windows.Token, error) {
	var token windows.Token
	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|
			windows.TOKEN_QUERY|
			windows.TOKEN_ASSIGN_PRIMARY,
		&token,
	)
	if err != nil {
		return 0, fmt.Errorf("open launcher process token: %w", err)
	}
	return token, nil
}

func createLUARestrictedToken(source windows.Token) (windows.Token, error) {
	var token windows.Token
	result, _, callErr := createRestrictedToken.Call(
		uintptr(source),
		uintptr(createRestrictedTokenLUA),
		0,
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&token)),
	)
	if result == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == 0 {
			callErr = windows.ERROR_INVALID_FUNCTION
		}
		return 0, fmt.Errorf(
			"CreateRestrictedToken(LUA_TOKEN): %w", callErr,
		)
	}
	return token, nil
}

func verifyLUARestrictedToken(token windows.Token) (uint32, error) {
	elevationType, err := launcherTokenElevationType(token)
	if err != nil {
		return 0, fmt.Errorf("inspect restricted token elevation type: %w", err)
	}
	if elevationType != tokenElevationTypeLimited {
		return 0, fmt.Errorf(
			"CreateRestrictedToken returned elevation type %d, want limited (%d)",
			elevationType,
			tokenElevationTypeLimited,
		)
	}
	enabled, exactDenyOnly, err := administratorGroupState(token)
	if err != nil {
		return 0, fmt.Errorf("inspect restricted token groups: %w", err)
	}
	if enabled || !exactDenyOnly {
		return 0, fmt.Errorf(
			"restricted token Administrator group enabled=%v exact_deny_only=%v",
			enabled,
			exactDenyOnly,
		)
	}
	if token.IsElevated() {
		return 0, errors.New("restricted token still reports UAC elevation")
	}

	tokenSession, err := launcherTokenSessionID(token)
	if err != nil {
		return 0, fmt.Errorf("inspect restricted token session: %w", err)
	}
	var processSession uint32
	if err := windows.ProcessIdToSessionId(
		uint32(os.Getpid()), &processSession,
	); err != nil {
		return 0, fmt.Errorf("inspect launcher process session: %w", err)
	}
	if tokenSession != processSession {
		return 0, fmt.Errorf(
			"restricted token session %d differs from launcher session %d",
			tokenSession,
			processSession,
		)
	}
	return tokenSession, nil
}

func administratorGroupState(token windows.Token) (
	enabled bool,
	exactDenyOnly bool,
	err error,
) {
	administrators, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return false, false, err
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false, false, err
	}
	for _, group := range groups.AllGroups() {
		if !group.Sid.Equals(administrators) {
			continue
		}
		return group.Attributes&windows.SE_GROUP_ENABLED != 0 &&
				group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0,
			group.Attributes == windows.SE_GROUP_USE_FOR_DENY_ONLY,
			nil
	}
	return false, false, nil
}

func launcherTokenElevationType(token windows.Token) (uint32, error) {
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

func launcherTokenSessionID(token windows.Token) (uint32, error) {
	var sessionID uint32
	var returned uint32
	err := windows.GetTokenInformation(
		token,
		windows.TokenSessionId,
		(*byte)(unsafe.Pointer(&sessionID)),
		uint32(unsafe.Sizeof(sessionID)),
		&returned,
	)
	if err != nil {
		return 0, err
	}
	if returned != uint32(unsafe.Sizeof(sessionID)) {
		return 0, windows.ERROR_BAD_LENGTH
	}
	return sessionID, nil
}

func launchCommandAsUser(
	token windows.Token,
	command []string,
) (uint32, error) {
	executable, err := exec.LookPath(command[0])
	if err != nil {
		return 0, fmt.Errorf("resolve child executable %q: %w", command[0], err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return 0, fmt.Errorf("resolve absolute child executable: %w", err)
	}
	arguments := append([]string{executable}, command[1:]...)
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, fmt.Errorf("encode child executable: %w", err)
	}
	commandLine, err := windows.UTF16PtrFromString(
		windows.ComposeCommandLine(arguments),
	)
	if err != nil {
		return 0, fmt.Errorf("encode child command line: %w", err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return 0, fmt.Errorf("resolve child working directory: %w", err)
	}
	currentDirectory, err := windows.UTF16PtrFromString(workingDirectory)
	if err != nil {
		return 0, fmt.Errorf("encode child working directory: %w", err)
	}
	desktop, err := windows.UTF16PtrFromString(
		interactiveWindowStationDesktop,
	)
	if err != nil {
		return 0, fmt.Errorf("encode interactive desktop: %w", err)
	}

	standardHandles, err := duplicateLauncherStandardHandles()
	if err != nil {
		return 0, err
	}
	defer standardHandles.close()
	startup := windows.StartupInfo{
		Cb:        uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:   desktop,
		Flags:     windows.STARTF_USESTDHANDLES,
		StdInput:  standardHandles.input,
		StdOutput: standardHandles.output,
		StdErr:    standardHandles.errput,
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcessAsUser(
		token,
		application,
		commandLine,
		nil,
		nil,
		true,
		0,
		nil,
		currentDirectory,
		&startup,
		&process,
	); err != nil {
		return 0, fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)

	event, err := windows.WaitForSingleObject(process.Process, windows.INFINITE)
	if err != nil {
		return 0, fmt.Errorf("wait for limited child: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return 0, fmt.Errorf("wait for limited child returned %#x", event)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process.Process, &exitCode); err != nil {
		return 0, fmt.Errorf("read limited child exit code: %w", err)
	}
	return exitCode, nil
}

type launcherStandardHandles struct {
	input  windows.Handle
	output windows.Handle
	errput windows.Handle
}

func duplicateLauncherStandardHandles() (launcherStandardHandles, error) {
	var handles launcherStandardHandles
	for _, candidate := range []struct {
		name   string
		kind   uint32
		target *windows.Handle
	}{
		{name: "stdin", kind: windows.STD_INPUT_HANDLE, target: &handles.input},
		{name: "stdout", kind: windows.STD_OUTPUT_HANDLE, target: &handles.output},
		{name: "stderr", kind: windows.STD_ERROR_HANDLE, target: &handles.errput},
	} {
		source, err := windows.GetStdHandle(candidate.kind)
		if err != nil {
			handles.close()
			return launcherStandardHandles{}, fmt.Errorf(
				"open launcher %s: %w", candidate.name, err,
			)
		}
		if err := windows.DuplicateHandle(
			windows.CurrentProcess(),
			source,
			windows.CurrentProcess(),
			candidate.target,
			0,
			true,
			windows.DUPLICATE_SAME_ACCESS,
		); err != nil {
			handles.close()
			return launcherStandardHandles{}, fmt.Errorf(
				"duplicate launcher %s: %w", candidate.name, err,
			)
		}
	}
	return handles, nil
}

func (handles *launcherStandardHandles) close() {
	for _, handle := range []windows.Handle{
		handles.input, handles.output, handles.errput,
	} {
		if handle != 0 && handle != windows.InvalidHandle {
			_ = windows.CloseHandle(handle)
		}
	}
	handles.input = 0
	handles.output = 0
	handles.errput = 0
}
