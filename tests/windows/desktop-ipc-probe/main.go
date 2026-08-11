//go:build windows

// desktop-ipc-probe exercises the unprivileged side of the Windows desktop
// elevation protocol without displaying a UAC prompt. It is intentionally a
// separate process so Windows CI can run it with a standard-user token while
// the installed production helper runs with the administrator runner token.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

const (
	desktopPipePrefix       = `\\.\pipe\wg-quic-desktop-`
	desktopOperationTimeout = 90 * time.Second
	desktopResultGrace      = 5 * time.Second
	maximumDesktopMessage   = 1024 * 1024
	wantCheckMessage        = "configuration is valid for wg-quic-quick"
)

type options struct {
	name       string
	readyPath  string
	resultPath string
}

type readyRecord struct {
	Pipe     string `json:"pipe"`
	PID      int    `json:"pid"`
	Elevated bool   `json:"elevated"`
}

type desktopRequest struct {
	Action             string `json:"action"`
	Name               string `json:"name"`
	Source             string `json:"source,omitempty"`
	Overwrite          bool   `json:"overwrite,omitempty"`
	DeadlineUnixMillis int64  `json:"deadline_unix_millis"`
}

type desktopResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err == nil {
		err = runProbe(opts)
	}
	if opts.resultPath != "" {
		outcome := "passed"
		if err != nil {
			outcome = "failed: " + err.Error()
		}
		if resultErr := writeAtomic(opts.resultPath, []byte(outcome+"\n")); resultErr != nil {
			err = errors.Join(err, fmt.Errorf("write result: %w", resultErr))
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "desktop-ipc-probe:", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("desktop-ipc-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.name, "name", "", "installed tunnel name to check")
	flags.StringVar(&opts.readyPath, "ready", "", "path for the ready JSON record")
	flags.StringVar(&opts.resultPath, "result", "", "path for the final result record")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, errors.New("unexpected positional arguments")
	}
	if opts.name == "" {
		return opts, errors.New("-name is required")
	}
	if opts.readyPath == "" {
		return opts, errors.New("-ready is required")
	}
	if opts.resultPath == "" {
		return opts, errors.New("-result is required")
	}
	return opts, nil
}

func runProbe(opts options) error {
	elevated, err := currentProcessElevated()
	if err != nil {
		return err
	}
	if elevated {
		return errors.New("probe unexpectedly has an Administrator token")
	}

	pipePath, err := randomDesktopPipePath()
	if err != nil {
		return err
	}
	// A nil security descriptor deliberately matches RunWindowsDesktopClient:
	// Windows supplies its default named-pipe ACL for the standard-user owner.
	listener, err := (&namedpipe.ListenConfig{
		InputBufferSize:  64 * 1024,
		OutputBufferSize: 64 * 1024,
	}).Listen(pipePath)
	if err != nil {
		return fmt.Errorf("listen on desktop pipe: %w", err)
	}
	defer listener.Close()
	request, operationDeadline := newDesktopCheckRequest(opts.name, time.Now())

	ready, err := json.Marshal(readyRecord{
		Pipe: pipePath, PID: os.Getpid(), Elevated: elevated,
	})
	if err != nil {
		return fmt.Errorf("encode ready record: %w", err)
	}
	if err := writeAtomic(opts.readyPath, append(ready, '\n')); err != nil {
		return fmt.Errorf("write ready record: %w", err)
	}

	connection, err := acceptBefore(
		listener,
		operationDeadline,
	)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(operationDeadline.Add(desktopResultGrace))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("send desktop check request: %w", err)
	}
	limited := &io.LimitedReader{R: connection, N: maximumDesktopMessage + 1}
	var result desktopResult
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return fmt.Errorf("decode desktop helper result: %w", err)
	}
	if limited.N == 0 {
		return errors.New("desktop helper result exceeded 1 MiB")
	}
	if !result.Success {
		return fmt.Errorf("desktop helper rejected check: %s", result.Message)
	}
	if strings.TrimSpace(result.Message) != wantCheckMessage {
		return fmt.Errorf(
			"desktop helper check message = %q, want %q",
			result.Message,
			wantCheckMessage,
		)
	}
	return nil
}

func newDesktopCheckRequest(name string, now time.Time) (desktopRequest, time.Time) {
	deadline := now.Add(desktopOperationTimeout)
	return desktopRequest{
		Action:             "check",
		Name:               name,
		DeadlineUnixMillis: deadline.UnixMilli(),
	}, deadline
}

func currentProcessElevated() (bool, error) {
	token := windows.GetCurrentProcessToken()
	administrators, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return false, fmt.Errorf("resolve Administrators SID: %w", err)
	}
	member, err := token.IsMember(administrators)
	if err != nil {
		return false, fmt.Errorf("inspect process token membership: %w", err)
	}
	return token.IsElevated() || member, nil
}

func randomDesktopPipePath() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("create desktop pipe name: %w", err)
	}
	return fmt.Sprintf(
		"%s%d-%s",
		desktopPipePrefix,
		os.Getpid(),
		hex.EncodeToString(nonce[:]),
	), nil
}

func acceptBefore(listener net.Listener, deadline time.Time) (net.Conn, error) {
	type acceptResult struct {
		connection net.Conn
		err        error
	}
	result := make(chan acceptResult, 1)
	go func() {
		connection, err := listener.Accept()
		result <- acceptResult{connection: connection, err: err}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case accepted := <-result:
		if accepted.err != nil {
			return nil, fmt.Errorf("accept desktop helper connection: %w", accepted.err)
		}
		return accepted.connection, nil
	case <-timer.C:
		_ = listener.Close()
		return nil, errors.New("timed out waiting for desktop helper connection")
	}
}

func writeAtomic(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".desktop-ipc-probe-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
