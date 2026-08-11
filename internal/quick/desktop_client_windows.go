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
	`$process = Start-Process ` +
	`-FilePath $env:WG_QUIC_ELEVATED_EXE ` +
	`-ArgumentList @('desktop-helper') ` +
	`-Verb RunAs -Wait -PassThru -WindowStyle Hidden; ` +
	`exit $process.ExitCode`

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
	pipePath, err := randomWindowsDesktopPipePath()
	if err != nil {
		return "", err
	}
	listener, err := (&namedpipe.ListenConfig{
		InputBufferSize:  64 * 1024,
		OutputBufferSize: 64 * 1024,
	}).Listen(pipePath)
	if err != nil {
		return "", fmt.Errorf("listen for desktop helper result: %w", err)
	}
	defer listener.Close()

	resultChannel := make(chan windowsDesktopResult, 1)
	resultError := make(chan error, 1)
	go readWindowsDesktopResult(listener, resultChannel, resultError)

	operationContext, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
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
		windowsDesktopActionEnv+"="+action,
		windowsDesktopNameEnv+"="+name,
		windowsDesktopSourceEnv+"="+source,
		windowsDesktopOverwriteEnv+"="+boolEnvironmentValue(overwrite),
		windowsDesktopResultPipeEnv+"="+pipePath,
		"WG_QUIC_ELEVATED_EXE="+currentExecutablePath(),
	)
	launchOutput, launchErr := command.CombinedOutput()
	if operationContext.Err() != nil {
		return "", fmt.Errorf(
			"elevated desktop operation timed out after 90 seconds: %w",
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
		case <-time.After(time.Second):
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
		return "", errors.New(strings.TrimSpace(result.Message))
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
	case "up", "down", "check":
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
		return "", fmt.Errorf("create desktop result pipe name: %w", err)
	}
	return fmt.Sprintf(
		`\\.\pipe\wg-quic-desktop-%d-%s`,
		os.Getpid(),
		hex.EncodeToString(nonce[:]),
	), nil
}

func readWindowsDesktopResult(
	listener net.Listener,
	resultChannel chan<- windowsDesktopResult,
	errorChannel chan<- error,
) {
	connection, err := listener.Accept()
	if err != nil {
		errorChannel <- fmt.Errorf("accept desktop helper result: %w", err)
		return
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(90 * time.Second))
	limited := &io.LimitedReader{
		R: connection,
		N: maxWindowsDesktopConfigSize + 1,
	}
	var result windowsDesktopResult
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		errorChannel <- fmt.Errorf("decode desktop helper result: %w", err)
		return
	}
	if limited.N == 0 {
		errorChannel <- errors.New("desktop helper result exceeded 1 MiB")
		return
	}
	resultChannel <- result
}

func windowsDesktopLauncherError(message string, resultErr error) error {
	if strings.Contains(strings.ToLower(message), "canceled") ||
		strings.Contains(strings.ToLower(message), "cancelled") ||
		strings.Contains(message, "1223") {
		return errors.New("administrator approval was canceled")
	}
	if message == "" {
		message = "the elevated wg-quic operation failed"
	}
	if resultErr != nil {
		return errors.Join(errors.New(message), resultErr)
	}
	return errors.New(message)
}

func boolEnvironmentValue(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func currentExecutablePath() string {
	path, err := os.Executable()
	if err != nil {
		return "wg-quic-quick.exe"
	}
	return path
}
