//go:build windows

package quick

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"golang.org/x/sys/windows/svc"
)

func TestWindowsManagementServiceReportsReadyAndStopsCleanly(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 4)
	service := &windowsManagementService{
		serve: func(ctx context.Context, ready func()) error {
			ready()
			<-ctx.Done()
			return nil
		},
	}
	result := make(chan uint32, 1)
	go func() {
		_, code := service.Execute(nil, requests, changes)
		result <- code
	}()
	expectWindowsServiceState(t, changes, svc.StartPending)
	expectWindowsServiceState(t, changes, svc.Running)
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	expectWindowsServiceState(t, changes, svc.StopPending)
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("clean management service stop exit code = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Windows management service did not stop")
	}
}

func TestWindowsManagementServiceStartupReportsProgress(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 16)
	releaseStartup := make(chan struct{})
	service := &windowsManagementService{
		heartbeatInterval: 5 * time.Millisecond,
		serve: func(ctx context.Context, ready func()) error {
			select {
			case <-releaseStartup:
				ready()
			case <-ctx.Done():
				return nil
			}
			<-ctx.Done()
			return nil
		},
	}
	result := make(chan uint32, 1)
	go func() {
		_, code := service.Execute(nil, requests, changes)
		result <- code
	}()
	first := <-changes
	if first.State != svc.StartPending || first.CheckPoint == 0 ||
		first.WaitHint == 0 {
		t.Fatalf("initial management startup status = %#v", first)
	}
	select {
	case next := <-changes:
		if next.State != svc.StartPending ||
			next.CheckPoint <= first.CheckPoint || next.WaitHint == 0 {
			t.Fatalf("management startup progress = %#v after %#v", next, first)
		}
	case <-time.After(time.Second):
		t.Fatal("management startup did not report a checkpoint")
	}
	close(releaseStartup)
	expectWindowsServiceState(t, changes, svc.Running)
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	expectWindowsServiceState(t, changes, svc.StopPending)
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("management startup progress stop exit code = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("management startup progress fixture did not stop")
	}
}

func TestWindowsManagementServiceFailsBeforeReady(t *testing.T) {
	changes := make(chan svc.Status, 2)
	service := &windowsManagementService{
		serve: func(context.Context, func()) error {
			return errors.New("startup failed")
		},
	}
	_, code := service.Execute(nil, make(chan svc.ChangeRequest), changes)
	if code != 1 {
		t.Fatalf("failed management service exit code = %d, want 1", code)
	}
	expectWindowsServiceState(t, changes, svc.StartPending)
}

func TestWindowsManagementServiceShutdownIsBoundedWithProgress(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 32)
	release := make(chan struct{})
	defer close(release)
	var logs []string
	service := &windowsManagementService{
		shutdownTimeout:   60 * time.Millisecond,
		heartbeatInterval: 10 * time.Millisecond,
		logf: func(format string, arguments ...any) {
			logs = append(logs, format)
		},
		serve: func(_ context.Context, ready func()) error {
			ready()
			<-release
			return nil
		},
	}
	result := make(chan uint32, 1)
	go func() {
		_, code := service.Execute(nil, requests, changes)
		result <- code
	}()
	expectWindowsServiceState(t, changes, svc.StartPending)
	expectWindowsServiceState(t, changes, svc.Running)
	requests <- svc.ChangeRequest{Cmd: svc.Stop}

	var checkpoints []uint32
	for collecting := true; collecting; {
		select {
		case status := <-changes:
			if status.State != svc.StopPending {
				t.Fatalf("management shutdown state = %v", status.State)
			}
			checkpoints = append(checkpoints, status.CheckPoint)
		case code := <-result:
			if code != 1 {
				t.Fatalf("management shutdown exit code = %d, want 1", code)
			}
			collecting = false
		case <-time.After(time.Second):
			t.Fatal("management shutdown timeout did not fire")
		}
	}
	if len(checkpoints) < 2 {
		t.Fatalf("management shutdown checkpoints = %v", checkpoints)
	}
	for index := 1; index < len(checkpoints); index++ {
		if checkpoints[index] <= checkpoints[index-1] {
			t.Fatalf("management checkpoints did not advance: %v", checkpoints)
		}
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "timed out") {
		t.Fatalf("management shutdown logs = %q", logs)
	}
}

func TestValidateWindowsManagementProbeIsReadOnly(t *testing.T) {
	valid := windowsManagementRequest{Action: "probe"}
	if err := validateWindowsManagementRequest(valid); err != nil {
		t.Fatalf("valid management probe: %v", err)
	}
	for _, request := range []windowsManagementRequest{
		{Action: "probe", Name: "wg0"},
		{Action: "probe", Overwrite: true},
		{Action: "probe", Config: []byte("data")},
	} {
		if err := validateWindowsManagementRequest(request); err == nil {
			t.Fatalf("accepted stateful management probe %#v", request)
		}
	}
}

func TestWindowsManagementRejectsHooksWithElevationFallback(t *testing.T) {
	const key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	for _, hook := range []string{"PreUp", "PostUp", "PreDown", "PostDown"} {
		t.Run(hook, func(t *testing.T) {
			contents := []byte("[Interface]\nPrivateKey = " + key +
				"\n" + hook + " = whoami\n")
			err := validateWindowsManagementConfigBytes(contents)
			if !errors.Is(err, errWindowsManagementElevationRequired) {
				t.Fatalf("management hook error = %v", err)
			}
			if !shouldUseWindowsDesktopElevationFallback(err) {
				t.Fatal("management hook did not request explicit elevation")
			}
		})
	}
	if err := validateWindowsManagementConfigBytes([]byte(
		"[Interface]\nPrivateKey = " + key + "\n",
	)); err != nil {
		t.Fatalf("hook-free management configuration: %v", err)
	}
}

func TestWindowsManagementElevationResultIsStructured(t *testing.T) {
	err := windowsManagementResultError(windowsManagementResult{
		ProtocolVersion: windowsManagementProtocolVersion,
		Code:            windowsManagementCodeElevation,
		Message:         "hooks need approval",
	})
	if !errors.Is(err, errWindowsManagementElevationRequired) {
		t.Fatalf("elevation result error = %v", err)
	}
	if got := windowsManagementFailureCode(
		err, windowsManagementCodeInvalidRequest,
	); got != windowsManagementCodeElevation {
		t.Fatalf("elevation failure code = %q", got)
	}
}

func TestWindowsManagementDialUsesShortHandshakeContext(t *testing.T) {
	original := windowsManagementDial
	defer func() { windowsManagementDial = original }()
	var remaining time.Duration
	windowsManagementDial = func(ctx context.Context) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, errors.New("dial context has no deadline")
		}
		remaining = time.Until(deadline)
		return nil, errors.New("not listening")
	}
	operationContext, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()
	_, err := runWindowsManagementClient(
		operationContext, "probe", "", "", false,
	)
	if !errors.Is(err, errWindowsManagementUnavailable) {
		t.Fatalf("short broker dial error = %v", err)
	}
	if remaining <= 0 || remaining > windowsManagementHandshakeTimeout {
		t.Fatalf("broker dial deadline remaining = %s", remaining)
	}
	if operationContext.Err() != nil {
		t.Fatalf("broker dial consumed operation context: %v", operationContext.Err())
	}
}

func TestWindowsManagementUnauthorizedLargeImportSendsNoRequest(t *testing.T) {
	original := windowsManagementDial
	defer func() { windowsManagementDial = original }()
	client, server := net.Pipe()
	windowsManagementDial = func(context.Context) (net.Conn, error) {
		return client, nil
	}
	serverResult := make(chan error, 1)
	go func() {
		defer server.Close()
		if err := readWindowsManagementPreamble(server); err != nil {
			serverResult <- err
			return
		}
		if err := writeWindowsManagementResult(server, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Code:            windowsManagementCodeUnauthorized,
			Message:         "approval required",
		}); err != nil {
			serverResult <- err
			return
		}
		_ = server.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		buffer := make([]byte, 1)
		n, err := server.Read(buffer)
		if n != 0 {
			serverResult <- fmt.Errorf(
				"client sent request bytes before authorization: %x", buffer[:n],
			)
			return
		}
		if err == nil {
			serverResult <- errors.New("unauthorized client read unexpectedly succeeded")
			return
		}
		serverResult <- nil
	}()

	var configuration strings.Builder
	configuration.WriteString("[Interface]\n")
	configuration.WriteString(
		"PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n",
	)
	padding := "# " + strings.Repeat("x", 1022) + "\n"
	for configuration.Len()+len(padding) < 900*1024 {
		configuration.WriteString(padding)
	}
	source := filepath.Join(t.TempDir(), "large.conf")
	if err := os.WriteFile(source, []byte(configuration.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	operationContext, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()
	_, err := runWindowsManagementClient(
		operationContext, "import", "large", source, false,
	)
	if !errors.Is(err, errWindowsManagementUnauthorized) {
		t.Fatalf("large unauthorized import error = %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsManagementAuthorizedMaximumImportRoundTrip(t *testing.T) {
	original := windowsManagementDial
	defer func() { windowsManagementDial = original }()
	client, server := net.Pipe()
	windowsManagementDial = func(context.Context) (net.Conn, error) {
		return client, nil
	}
	configuration := []byte(
		"[Interface]\n" +
			"PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n",
	)
	padding := bytes.Repeat(
		[]byte("# xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"),
		(maxWindowsDesktopConfigSize-len(configuration))/66,
	)
	configuration = append(configuration, padding...)
	if len(configuration) > maxWindowsDesktopConfigSize {
		t.Fatalf("maximum import fixture is %d bytes", len(configuration))
	}
	source := filepath.Join(t.TempDir(), "maximum.conf")
	if err := os.WriteFile(source, configuration, 0o600); err != nil {
		t.Fatal(err)
	}

	serverResult := make(chan error, 1)
	go func() {
		defer server.Close()
		if err := readWindowsManagementPreamble(server); err != nil {
			serverResult <- err
			return
		}
		if err := writeWindowsManagementResult(server, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Success:         true,
			Code:            windowsManagementCodeContinue,
		}); err != nil {
			serverResult <- err
			return
		}
		request, err := readWindowsManagementRequest(server)
		if err != nil {
			serverResult <- err
			return
		}
		if request.Action != "import" || request.Name != "maximum" ||
			!request.Overwrite {
			serverResult <- fmt.Errorf("unexpected request %#v", request)
			return
		}
		if !bytes.Equal(request.Config, configuration) {
			serverResult <- fmt.Errorf(
				"configuration round trip = %d bytes, want %d",
				len(request.Config), len(configuration),
			)
			return
		}
		serverResult <- writeWindowsManagementResult(
			server,
			windowsManagementResult{
				ProtocolVersion: windowsManagementProtocolVersion,
				Success:         true,
				Code:            windowsManagementCodeOK,
			},
		)
	}()

	operationContext, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()
	if _, err := runWindowsManagementClient(
		operationContext, "import", "maximum", source, true,
	); err != nil {
		t.Fatalf("maximum authorized import: %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsManagementIncompatibleHandshakeSendsNoRequest(t *testing.T) {
	original := windowsManagementDial
	defer func() { windowsManagementDial = original }()
	client, server := net.Pipe()
	windowsManagementDial = func(context.Context) (net.Conn, error) {
		return client, nil
	}
	serverResult := make(chan error, 1)
	go func() {
		defer server.Close()
		if err := readWindowsManagementPreamble(server); err != nil {
			serverResult <- err
			return
		}
		if err := writeWindowsManagementResult(server, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion + 1,
			Success:         true,
			Code:            windowsManagementCodeContinue,
		}); err != nil {
			serverResult <- err
			return
		}
		_ = server.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		buffer := make([]byte, 1)
		n, err := server.Read(buffer)
		if n != 0 {
			serverResult <- errors.New("incompatible client sent a request")
			return
		}
		if err == nil {
			serverResult <- errors.New("incompatible client remained connected")
			return
		}
		serverResult <- nil
	}()
	operationContext, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()
	_, err := runWindowsManagementClient(
		operationContext, "probe", "", "", false,
	)
	if !errors.Is(err, errWindowsManagementIncompatible) {
		t.Fatalf("incompatible handshake error = %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsManagementResponseLossDoesNotRetryMutation(t *testing.T) {
	original := windowsManagementDial
	defer func() { windowsManagementDial = original }()
	client, server := net.Pipe()
	windowsManagementDial = func(context.Context) (net.Conn, error) {
		return client, nil
	}
	serverResult := make(chan error, 1)
	go func() {
		defer server.Close()
		if err := readWindowsManagementPreamble(server); err != nil {
			serverResult <- err
			return
		}
		if err := writeWindowsManagementResult(server, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Success:         true,
			Code:            windowsManagementCodeContinue,
		}); err != nil {
			serverResult <- err
			return
		}
		var request windowsManagementRequest
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			serverResult <- err
			return
		}
		if request.Action != "down" || request.Name != "office" {
			serverResult <- fmt.Errorf("unexpected request %#v", request)
			return
		}
		serverResult <- nil
	}()
	operationContext, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()
	_, err := runWindowsManagementClient(
		operationContext, "down", "office", "", false,
	)
	if !errors.Is(err, errWindowsManagementOutcomeUnknown) {
		t.Fatalf("lost mutation response error = %v", err)
	}
	if shouldUseWindowsDesktopElevationFallback(err) {
		t.Fatal("lost mutation response permits a duplicate UAC attempt")
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsManagementPostDispatchVersionMismatchDoesNotRetryMutation(
	t *testing.T,
) {
	original := windowsManagementDial
	defer func() { windowsManagementDial = original }()
	client, server := net.Pipe()
	windowsManagementDial = func(context.Context) (net.Conn, error) {
		return client, nil
	}
	serverResult := make(chan error, 1)
	go func() {
		defer server.Close()
		if err := readWindowsManagementPreamble(server); err != nil {
			serverResult <- err
			return
		}
		if err := writeWindowsManagementResult(server, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Success:         true,
			Code:            windowsManagementCodeContinue,
		}); err != nil {
			serverResult <- err
			return
		}
		var request windowsManagementRequest
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			serverResult <- err
			return
		}
		if request.Action != "down" || request.Name != "office" {
			serverResult <- fmt.Errorf("unexpected request %#v", request)
			return
		}
		serverResult <- writeWindowsManagementResult(server, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion + 1,
			Success:         true,
			Code:            windowsManagementCodeOK,
		})
	}()
	operationContext, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()
	_, err := runWindowsManagementClient(
		operationContext, "down", "office", "", false,
	)
	if !errors.Is(err, errWindowsManagementOutcomeUnknown) {
		t.Fatalf("post-dispatch version error = %v", err)
	}
	if shouldUseWindowsDesktopElevationFallback(err) {
		t.Fatal("post-dispatch version mismatch permits a duplicate UAC attempt")
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsManagementUpRechecksStoredHooks(t *testing.T) {
	original := windowsManagementOpenStoredConfig
	defer func() { windowsManagementOpenStoredConfig = original }()
	const configuration = `[Interface]
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
PreUp = whoami
`
	cfg, err := config.Parse(strings.NewReader(configuration))
	if err != nil {
		t.Fatal(err)
	}
	windowsManagementOpenStoredConfig = func(
		name string,
	) (*windowsStoredConfigLease, *config.Config, error) {
		if name != "hooked" {
			t.Fatalf("stored config name = %q", name)
		}
		return nil, cfg, nil
	}
	var mutations sync.Mutex
	err = runWindowsManagementOperation(
		context.Background(),
		windowsManagementRequest{Action: "up", Name: "hooked"},
		&mutations,
	)
	if !errors.Is(err, errWindowsManagementElevationRequired) {
		t.Fatalf("management up hook error = %v", err)
	}
}

func TestWindowsManagementCheckUsesSecureStoredConfigOpen(t *testing.T) {
	original := windowsManagementOpenStoredConfig
	defer func() { windowsManagementOpenStoredConfig = original }()
	want := errors.New("secure stored configuration rejected")
	windowsManagementOpenStoredConfig = func(
		name string,
	) (*windowsStoredConfigLease, *config.Config, error) {
		if name != "office" {
			t.Fatalf("stored config name = %q", name)
		}
		return nil, nil, want
	}
	var mutations sync.Mutex
	err := runWindowsManagementOperation(
		context.Background(),
		windowsManagementRequest{Action: "check", Name: "office"},
		&mutations,
	)
	if !errors.Is(err, want) {
		t.Fatalf("management check error = %v", err)
	}
}

func TestWindowsBrokerSafeServiceCommandProvenance(t *testing.T) {
	trusted := []string{
		`C:\ProgramData\wg-quic\runtime\nonce\wg-quic-quick.exe`,
		"run",
		"office",
		"--broker-safe",
	}
	if !windowsServiceCommandShapeIsBrokerSafe(trusted, "office") {
		t.Fatal("rejected broker-safe service command")
	}
	for _, arguments := range [][]string{
		trusted[:3],
		{trusted[0], "run", "office"},
		{trusted[0], "run", "other", "--broker-safe"},
		{trusted[0], "run", "office", "--other"},
		{`relative.exe`, "run", "office", "--broker-safe"},
	} {
		if windowsServiceCommandShapeIsBrokerSafe(arguments, "office") {
			t.Fatalf("accepted unsafe service command %#v", arguments)
		}
	}
}

func TestWindowsManagementRequestDeadlineRejectsExpiredAndClampsFuture(
	t *testing.T,
) {
	now := time.UnixMilli(1_800_000_000_000)
	if _, err := windowsManagementRequestDeadline(
		windowsManagementRequest{}, now,
	); err == nil {
		t.Fatal("accepted a management request without a deadline")
	}
	if _, err := windowsManagementRequestDeadline(windowsManagementRequest{
		DeadlineUnixMillis: now.UnixMilli(),
	}, now); err == nil {
		t.Fatal("accepted an expired management request")
	}
	deadline, err := windowsManagementRequestDeadline(
		windowsManagementRequest{
			DeadlineUnixMillis: now.Add(24 * time.Hour).UnixMilli(),
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(windowsDesktopOperationTimeout); !deadline.Equal(want) {
		t.Fatalf("management deadline = %s, want %s", deadline, want)
	}
}

func TestWindowsManagementPreambleAndResultAreBounded(t *testing.T) {
	var preamble bytes.Buffer
	preamble.Write(windowsManagementPreamble)
	if err := readWindowsManagementPreamble(&preamble); err != nil {
		t.Fatal(err)
	}
	if err := readWindowsManagementPreamble(
		bytes.NewBufferString("wrong!"),
	); err == nil {
		t.Fatal("accepted an invalid management preamble")
	}

	want := windowsManagementResult{
		ProtocolVersion: windowsManagementProtocolVersion,
		Success:         true,
		Code:            windowsManagementCodeOK,
		Message:         "ready",
	}
	var encoded bytes.Buffer
	if err := writeWindowsManagementResult(&encoded, want); err != nil {
		t.Fatal(err)
	}
	got, err := readWindowsManagementResult(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("management result = %#v, want %#v", got, want)
	}
	oversized := bytes.Repeat([]byte("x"), 64*1024+2)
	if _, err := readWindowsManagementResult(bytes.NewReader(oversized)); err == nil {
		t.Fatal("accepted an oversized management result")
	}
}

func TestWindowsDesktopBrokerStatusClassifiesExpectedStates(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status string
	}{
		{name: "ready", status: windowsManagementStatusReady},
		{
			name:   "unauthorized",
			err:    errWindowsManagementUnauthorized,
			status: windowsManagementStatusUnauthorized,
		},
		{
			name:   "unavailable",
			err:    errWindowsManagementUnavailable,
			status: windowsManagementStatusUnavailable,
		},
		{
			name:   "incompatible",
			err:    errWindowsManagementIncompatible,
			status: windowsManagementStatusIncompatible,
		},
		{name: "error", err: errors.New("boom"), status: windowsManagementStatusError},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := windowsDesktopBrokerStatus("detail", test.err)
			if got.Status != test.status {
				t.Fatalf("broker status = %q, want %q", got.Status, test.status)
			}
			if got.ServiceName != windowsManagementServiceName ||
				got.ProtocolVersion != windowsManagementProtocolVersion {
				t.Fatalf("broker metadata = %#v", got)
			}
		})
	}
}

func TestWindowsDesktopFallbackIsLimitedToBrokerBoundaryFailures(t *testing.T) {
	for _, err := range []error{
		errWindowsManagementUnavailable,
		errWindowsManagementUnauthorized,
		errWindowsManagementIncompatible,
		errWindowsManagementElevationRequired,
	} {
		if !shouldUseWindowsDesktopElevationFallback(err) {
			t.Fatalf("broker boundary error %v does not allow UAC fallback", err)
		}
	}
	if shouldUseWindowsDesktopElevationFallback(errors.New("operation failed")) {
		t.Fatal("operation failure unexpectedly allows a second privileged attempt")
	}
	if shouldUseWindowsDesktopElevationFallback(
		errWindowsManagementOutcomeUnknown,
	) {
		t.Fatal("unknown mutation outcome unexpectedly allows a duplicate attempt")
	}
}
