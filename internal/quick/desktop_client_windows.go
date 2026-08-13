//go:build windows

package quick

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
)

const windowsDesktopElevationScript = `$ErrorActionPreference = 'Stop'; ` +
	`$pipePath = $env:WG_QUIC_DESKTOP_PIPE; ` +
	`$process = Start-Process ` +
	`-FilePath $env:WG_QUIC_ELEVATED_EXE ` +
	`-ArgumentList @('desktop-helper', $pipePath) ` +
	`-Verb RunAs -Wait -PassThru -WindowStyle Hidden; ` +
	`exit $process.ExitCode`

const windowsDesktopPipeEnv = "WG_QUIC_DESKTOP_PIPE"

const (
	windowsDesktopOperationTimeout = 90 * time.Second
	windowsDesktopResultGrace      = 5 * time.Second
)

type windowsDesktopRequest struct {
	Action             string `json:"action"`
	Name               string `json:"name"`
	Config             []byte `json:"config,omitempty"`
	Overwrite          bool   `json:"overwrite,omitempty"`
	DeadlineUnixMillis int64  `json:"deadline_unix_millis"`
}

type windowsDesktopResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// RunWindowsDesktopClient owns the unprivileged side of the narrow desktop
// elevation protocol. Keeping this protocol in Go lets small native shells
// such as Tauri reuse the same audited helper without implementing Windows
// named-pipe and UAC behavior themselves.
func RunWindowsDesktopClient(
	ctx context.Context,
	action string,
	name string,
	source string,
	overwrite bool,
) (string, error) {
	if err := validateWindowsDesktopRequest(action, name, source); err != nil {
		return "", err
	}
	operationContext, cancel := context.WithTimeout(
		ctx, windowsDesktopOperationTimeout,
	)
	defer cancel()
	message, err := runWindowsManagementClient(
		operationContext, action, name, source, overwrite,
	)
	if err == nil {
		return message, nil
	}
	if !shouldUseWindowsDesktopElevationFallback(err) {
		return "", err
	}
	return runWindowsElevatedDesktopClient(
		operationContext, action, name, source, overwrite,
	)
}

func runWindowsElevatedDesktopClient(
	operationContext context.Context,
	action string,
	name string,
	source string,
	overwrite bool,
) (string, error) {
	deadline, ok := operationContext.Deadline()
	if !ok {
		return "", errors.New("desktop operation context has no deadline")
	}
	var contents []byte
	if action == "import" {
		var err error
		contents, err = readWindowsDesktopConfig(source)
		if err != nil {
			return "", err
		}
		if err := validateWindowsDesktopConfigBytes(contents); err != nil {
			return "", err
		}
	}
	pipePath, err := randomWindowsDesktopPipePath()
	if err != nil {
		return "", err
	}
	listener, err := (&namedpipe.ListenConfig{
		InputBufferSize:  64 * 1024,
		OutputBufferSize: 64 * 1024,
	}).Listen(pipePath)
	if err != nil {
		return "", fmt.Errorf("listen for desktop helper connection: %w", err)
	}
	defer listener.Close()

	request := windowsDesktopRequest{
		Action: action, Name: name, Config: contents, Overwrite: overwrite,
		DeadlineUnixMillis: deadline.UnixMilli(),
	}
	resultChannel := make(chan windowsDesktopResult, 1)
	resultError := make(chan error, 1)
	go exchangeWindowsDesktopRequest(
		listener, request, resultChannel, resultError,
	)

	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	powershell := filepath.Join(
		systemRoot,
		"System32",
		"WindowsPowerShell",
		"v1.0",
		"powershell.exe",
	)
	command := exec.CommandContext(
		operationContext,
		powershell,
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-Command",
		windowsDesktopElevationScript,
	)
	command.Env = append(
		os.Environ(),
		windowsDesktopPipeEnv+"="+pipePath,
		"WG_QUIC_ELEVATED_EXE="+currentExecutablePath(),
	)
	launchOutput, launchErr := command.CombinedOutput()
	if operationContext.Err() != nil {
		return "", fmt.Errorf(
			"elevated desktop operation ended before completion: %w",
			operationContext.Err(),
		)
	}
	launcherMessage := strings.TrimSpace(string(launchOutput))
	if launchErr != nil && launcherMessage == "" {
		launcherMessage = launchErr.Error()
	}

	var result windowsDesktopResult
	if launchErr != nil {
		select {
		case result = <-resultChannel:
		case err := <-resultError:
			return "", windowsDesktopLauncherError(launcherMessage, err)
		case <-time.After(windowsDesktopResultGrace):
			return "", windowsDesktopLauncherError(launcherMessage, nil)
		}
	} else {
		select {
		case result = <-resultChannel:
		case err := <-resultError:
			return "", err
		case <-operationContext.Done():
			return "", operationContext.Err()
		}
	}
	if !result.Success {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = "the elevated wg-quic operation failed without an error message"
		}
		return "", errors.New(message)
	}
	if launchErr != nil {
		return "", windowsDesktopLauncherError(launcherMessage, launchErr)
	}
	return strings.TrimSpace(result.Message), nil
}

func validateWindowsDesktopRequest(
	action string,
	name string,
	source string,
) error {
	if err := platform.Current().ValidateInterfaceName(name); err != nil {
		return err
	}
	switch action {
	case "up", "down", "check", "delete":
		if source != "" {
			return fmt.Errorf("desktop %s does not accept a source path", action)
		}
	case "import":
		if source == "" {
			return errors.New("desktop import source is required")
		}
	default:
		return fmt.Errorf("unsupported desktop helper action %q", action)
	}
	return nil
}

func randomWindowsDesktopPipePath() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("create desktop pipe name: %w", err)
	}
	return fmt.Sprintf(
		`\\.\pipe\wg-quic-desktop-%d-%s`,
		os.Getpid(),
		hex.EncodeToString(nonce[:]),
	), nil
}

func exchangeWindowsDesktopRequest(
	listener net.Listener,
	request windowsDesktopRequest,
	resultChannel chan<- windowsDesktopResult,
	errorChannel chan<- error,
) {
	connection, err := listener.Accept()
	if err != nil {
		errorChannel <- fmt.Errorf("accept desktop helper connection: %w", err)
		return
	}
	defer connection.Close()
	result, err := exchangeWindowsDesktopConnection(connection, request)
	if err != nil {
		errorChannel <- err
		return
	}
	resultChannel <- result
}

func exchangeWindowsDesktopConnection(
	connection net.Conn,
	request windowsDesktopRequest,
) (windowsDesktopResult, error) {
	_ = connection.SetDeadline(time.Now().Add(90 * time.Second))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return windowsDesktopResult{}, fmt.Errorf(
			"send desktop helper request: %w", err,
		)
	}
	limited := &io.LimitedReader{
		R: connection,
		N: maxWindowsDesktopConfigSize + 1,
	}
	var result windowsDesktopResult
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return windowsDesktopResult{}, fmt.Errorf(
			"decode desktop helper result: %w", err,
		)
	}
	if limited.N == 0 {
		return windowsDesktopResult{}, errors.New(
			"desktop helper result exceeded 1 MiB",
		)
	}
	return result, nil
}

func windowsDesktopLauncherError(message string, resultErr error) error {
	if strings.Contains(strings.ToLower(message), "canceled") ||
		strings.Contains(strings.ToLower(message), "cancelled") ||
		strings.Contains(message, "1223") {
		return errors.New("administrator approval was canceled")
	}
	if message == "" {
		message = "the elevated wg-quic helper exited before completing the IPC handshake"
	} else {
		message = "the elevated wg-quic helper exited before completing the IPC handshake: " + message
	}
	if resultErr != nil {
		return errors.Join(errors.New(message), resultErr)
	}
	return errors.New(message)
}

func currentExecutablePath() string {
	path, err := os.Executable()
	if err != nil {
		return "wg-quic-quick.exe"
	}
	return path
}
