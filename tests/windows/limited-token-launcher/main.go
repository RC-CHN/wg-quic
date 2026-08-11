//go:build windows

// limited-token-launcher obtains a genuine UAC-filtered Administrator token
// and starts a command with it in the launcher's current interactive session.
// It exists only as a Windows integration-test fixture; production privilege
// transitions remain owned by wg-quic.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createRestrictedTokenLUA  = 0x00000004
	tokenElevationTypeDefault = 1
	tokenElevationTypeLimited = 3
	limitedTokenLauncherUsage = "limited-token-launcher -- command [argument ...]"
	launcherTokenAccess       = windows.TOKEN_QUERY |
		windows.TOKEN_DUPLICATE |
		windows.TOKEN_ASSIGN_PRIMARY
)

var createRestrictedToken = windows.NewLazySystemDLL(
	"advapi32.dll",
).NewProc("CreateRestrictedToken")

var createProcessWithToken = windows.NewLazySystemDLL(
	"advapi32.dll",
).NewProc("CreateProcessWithTokenW")

func main() {
	command, err := parseCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, limitedTokenLauncherUsage)
		fmt.Fprintln(os.Stderr, "limited-token-launcher:", err)
		os.Exit(2)
	}
	exitCode, sessionID, tokenSource, err := runLimitedCommand(command)
	if err != nil {
		fmt.Fprintln(os.Stderr, "limited-token-launcher:", err)
		os.Exit(1)
	}
	fmt.Fprintf(
		os.Stderr,
		"limited-token-launcher: child completed in session %d with exit code %d using %s\n",
		sessionID,
		exitCode,
		tokenSource,
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

func runLimitedCommand(command []string) (uint32, uint32, string, error) {
	source, err := openLauncherSourceToken()
	if err != nil {
		return 0, 0, "", err
	}
	defer source.Close()

	enabled, exactDenyOnly, err := administratorGroupState(source)
	if err != nil {
		return 0, 0, "", fmt.Errorf("inspect launcher source token: %w", err)
	}
	if !enabled || exactDenyOnly {
		return 0, 0, "", errors.New(
			"launcher requires a full Administrator source token",
		)
	}

	limited, tokenSource, err := openLauncherLimitedToken(source)
	if err != nil {
		return 0, 0, "", err
	}
	defer limited.Close()
	sessionID, err := verifyLUARestrictedToken(limited)
	if err != nil {
		return 0, 0, "", err
	}

	exitCode, err := launchCommandWithToken(limited, command)
	if err != nil {
		return 0, sessionID, tokenSource, err
	}
	return exitCode, sessionID, tokenSource, nil
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

// openLauncherLimitedToken prefers the real filtered token owned by the
// interactive shell in this same user session. If no shell token is available,
// it asks Windows to derive a LUA token from the launcher's full Administrator
// token. Service-hosted test agents can have a TokenElevationTypeDefault token
// with no linked UAC pair; on those hosts the derived token is restricted but
// still is not an authentic UAC limited token. Production code never borrows
// or manufactures tokens; this is strictly an installed-GUI test fixture.
func openLauncherLimitedToken(source windows.Token) (
	windows.Token,
	string,
	error,
) {
	shell, processID, shellErr := openInteractiveShellLimitedToken(source)
	if shellErr == nil {
		return shell, fmt.Sprintf("interactive shell process %d", processID), nil
	}

	derived, derivedErr := createLUARestrictedToken(source)
	if derivedErr == nil {
		if _, verifyErr := verifyLUARestrictedToken(derived); verifyErr == nil {
			return derived, "CreateRestrictedToken(LUA_TOKEN)", nil
		} else {
			derivedErr = verifyErr
		}
		_ = derived.Close()
	}

	return 0, "", fmt.Errorf(
		"derive authentic limited token: interactive shell: %v; LUA_TOKEN: %w",
		shellErr,
		derivedErr,
	)
}

func openInteractiveShellLimitedToken(source windows.Token) (
	windows.Token,
	uint32,
	error,
) {
	sourceUser, err := source.GetTokenUser()
	if err != nil {
		return 0, 0, fmt.Errorf("inspect launcher user: %w", err)
	}
	expectedSession, err := launcherTokenSessionID(source)
	if err != nil {
		return 0, 0, fmt.Errorf("inspect launcher session: %w", err)
	}

	candidates, candidateErr := interactiveShellProcessIDs(expectedSession)
	var failures []string
	if candidateErr != nil {
		failures = append(failures, candidateErr.Error())
	}
	for _, processID := range candidates {
		limited, err := duplicateInteractiveLimitedToken(
			processID,
			expectedSession,
			sourceUser.User.Sid,
		)
		if err == nil {
			return limited, processID, nil
		}
		failures = append(
			failures,
			fmt.Sprintf("process %d: %v", processID, err),
		)
	}
	if len(failures) == 0 {
		failures = append(failures, "no shell process was found")
	}
	return 0, 0, fmt.Errorf("find same-session limited shell token: %s", strings.Join(failures, "; "))
}

func interactiveShellProcessIDs(expectedSession uint32) ([]uint32, error) {
	var processIDs []uint32
	addCandidate := func(processID uint32) {
		if processID == 0 {
			return
		}
		for _, existing := range processIDs {
			if existing == processID {
				return
			}
		}
		var sessionID uint32
		if windows.ProcessIdToSessionId(processID, &sessionID) == nil &&
			sessionID == expectedSession {
			processIDs = append(processIDs, processID)
		}
	}

	if shellWindow := windows.GetShellWindow(); shellWindow != 0 {
		var processID uint32
		if _, err := windows.GetWindowThreadProcessId(
			shellWindow,
			&processID,
		); err == nil {
			addCandidate(processID)
		}
	}

	snapshot, err := windows.CreateToolhelp32Snapshot(
		windows.TH32CS_SNAPPROCESS,
		0,
	)
	if err != nil {
		return processIDs, fmt.Errorf("enumerate shell processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return processIDs, fmt.Errorf("read first shell process: %w", err)
	}
	for {
		if strings.EqualFold(
			windows.UTF16ToString(entry.ExeFile[:]),
			"explorer.exe",
		) {
			addCandidate(entry.ProcessID)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return processIDs, fmt.Errorf(
					"enumerate next shell process: %w",
					err,
				)
			}
			break
		}
	}
	return processIDs, nil
}

func duplicateInteractiveLimitedToken(
	processID uint32,
	expectedSession uint32,
	expectedUser *windows.SID,
) (windows.Token, error) {
	var processSession uint32
	if err := windows.ProcessIdToSessionId(
		processID,
		&processSession,
	); err != nil {
		return 0, fmt.Errorf("inspect process session: %w", err)
	}
	if processSession != expectedSession {
		return 0, fmt.Errorf(
			"session %d differs from launcher session %d",
			processSession,
			expectedSession,
		)
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		processID,
	)
	if err != nil {
		return 0, fmt.Errorf("open shell process: %w", err)
	}
	defer windows.CloseHandle(process)
	var candidate windows.Token
	if err := windows.OpenProcessToken(
		process,
		windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE,
		&candidate,
	); err != nil {
		return 0, fmt.Errorf("open shell token: %w", err)
	}
	defer candidate.Close()
	candidateUser, err := candidate.GetTokenUser()
	if err != nil {
		return 0, fmt.Errorf("inspect shell user: %w", err)
	}
	if !candidateUser.User.Sid.Equals(expectedUser) {
		return 0, errors.New("shell token belongs to another user")
	}
	if err := verifyInteractiveShellLimitedToken(candidate); err != nil {
		return 0, err
	}

	var duplicate windows.Token
	if err := windows.DuplicateTokenEx(
		candidate,
		launcherTokenAccess,
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&duplicate,
	); err != nil {
		return 0, fmt.Errorf("duplicate shell primary token: %w", err)
	}
	if err := verifyInteractiveShellLimitedToken(duplicate); err != nil {
		_ = duplicate.Close()
		return 0, fmt.Errorf("verify duplicated shell token: %w", err)
	}
	return duplicate, nil
}

func verifyInteractiveShellLimitedToken(token windows.Token) error {
	if _, err := verifyLUARestrictedToken(token); err != nil {
		return err
	}
	linked, err := token.GetLinkedToken()
	if err != nil {
		return fmt.Errorf("open shell token's linked token: %w", err)
	}
	defer linked.Close()
	linkedAdministrator, _, err := administratorGroupState(linked)
	if err != nil {
		return fmt.Errorf("inspect shell token's linked token: %w", err)
	}
	if !linkedAdministrator {
		return errors.New("shell token's linked token is not an Administrator")
	}
	return nil
}

func verifyLUARestrictedToken(token windows.Token) (uint32, error) {
	elevationType, err := launcherTokenElevationType(token)
	if err != nil {
		return 0, fmt.Errorf("inspect restricted token elevation type: %w", err)
	}
	if elevationType != tokenElevationTypeLimited {
		return 0, fmt.Errorf(
			"token elevation type is %d, want limited (%d)",
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

func launchCommandWithToken(
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
	var environment *uint16
	if err := windows.CreateEnvironmentBlock(
		&environment,
		token,
		true,
	); err != nil {
		return 0, fmt.Errorf("create inherited child environment: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(environment)

	standardHandles, err := duplicateLauncherStandardHandles()
	if err != nil {
		return 0, err
	}
	defer standardHandles.close()
	startup := windows.StartupInfo{
		Cb:        uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Flags:     windows.STARTF_USESTDHANDLES,
		StdInput:  standardHandles.input,
		StdOutput: standardHandles.output,
		StdErr:    standardHandles.errput,
	}
	var process windows.ProcessInformation
	result, _, callErr := createProcessWithToken.Call(
		uintptr(token),
		0,
		uintptr(unsafe.Pointer(application)),
		uintptr(unsafe.Pointer(commandLine)),
		uintptr(windows.CREATE_UNICODE_ENVIRONMENT),
		uintptr(unsafe.Pointer(environment)),
		uintptr(unsafe.Pointer(currentDirectory)),
		uintptr(unsafe.Pointer(&startup)),
		uintptr(unsafe.Pointer(&process)),
	)
	if result == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == 0 {
			callErr = windows.ERROR_INVALID_FUNCTION
		}
		return 0, fmt.Errorf("CreateProcessWithTokenW: %w", callErr)
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
