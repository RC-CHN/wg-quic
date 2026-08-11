//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsQuickServiceReportsReadyAndStopsCleanly(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 4)
	service := &windowsQuickService{
		run: func(ctx context.Context, ready func(), _ func(string)) error {
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
			t.Fatalf("clean service stop exit code = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Windows service did not stop")
	}
}

func TestWindowsQuickServiceStartupReportsProgressAndInterrogates(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 2)
	changes := make(chan svc.Status, 16)
	releaseStartup := make(chan struct{})
	service := &windowsQuickService{
		startupTimeout:    time.Second,
		heartbeatInterval: 10 * time.Millisecond,
		run: func(ctx context.Context, ready func(), _ func(string)) error {
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

	first := expectWindowsServiceStatus(t, changes)
	if first.State != svc.StartPending || first.CheckPoint == 0 ||
		first.WaitHint != uint32(windowsServiceWaitHint/time.Millisecond) {
		t.Fatalf("initial Windows service startup status = %#v", first)
	}
	requests <- svc.ChangeRequest{
		Cmd:           svc.Interrogate,
		CurrentStatus: svc.Status{State: svc.Running},
	}
	interrogated := expectWindowsServiceStatus(t, changes)
	if interrogated.State != svc.StartPending ||
		interrogated.CheckPoint < first.CheckPoint ||
		interrogated.WaitHint != first.WaitHint {
		t.Fatalf(
			"interrogated startup status = %#v after %#v",
			interrogated, first,
		)
	}

	var heartbeat svc.Status
	for heartbeat.CheckPoint <= interrogated.CheckPoint {
		heartbeat = expectWindowsServiceStatus(t, changes)
		if heartbeat.State != svc.StartPending {
			t.Fatalf("startup heartbeat state = %v", heartbeat.State)
		}
	}
	close(releaseStartup)
	for {
		status := expectWindowsServiceStatus(t, changes)
		if status.State == svc.Running {
			break
		}
		if status.State != svc.StartPending {
			t.Fatalf("startup transition state = %v", status.State)
		}
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	expectWindowsServiceState(t, changes, svc.StopPending)
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("startup progress service exit code = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("startup progress service did not stop")
	}
}

func TestWindowsQuickServiceStartupTimeoutIsBounded(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 64)
	canceled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var logs []string
	service := &windowsQuickService{
		startupTimeout:    50 * time.Millisecond,
		shutdownTimeout:   50 * time.Millisecond,
		heartbeatInterval: 10 * time.Millisecond,
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
		run: func(ctx context.Context, _ func(), _ func(string)) error {
			<-ctx.Done()
			close(canceled)
			<-release
			return nil
		},
	}
	result := make(chan uint32, 1)
	go func() {
		_, code := service.Execute(nil, requests, changes)
		result <- code
	}()

	var startupCheckpoints []uint32
	var stopCheckpoints []uint32
	for {
		select {
		case status := <-changes:
			switch status.State {
			case svc.StartPending:
				if status.WaitHint != uint32(windowsServiceWaitHint/time.Millisecond) {
					t.Fatalf("startup wait hint = %d", status.WaitHint)
				}
				startupCheckpoints = append(startupCheckpoints, status.CheckPoint)
			case svc.StopPending:
				stopCheckpoints = append(stopCheckpoints, status.CheckPoint)
			default:
				t.Fatalf("startup timeout state = %v", status.State)
			}
		case code := <-result:
			if code != 1 {
				t.Fatalf("startup timeout exit code = %d, want 1", code)
			}
			goto complete
		case <-time.After(time.Second):
			t.Fatal("startup timeout did not bound service execution")
		}
	}

complete:
	for draining := true; draining; {
		select {
		case status := <-changes:
			if status.State != svc.StopPending {
				t.Fatalf("status after startup timeout = %v", status.State)
			}
			stopCheckpoints = append(stopCheckpoints, status.CheckPoint)
		default:
			draining = false
		}
	}
	if len(startupCheckpoints) < 2 {
		t.Fatalf(
			"startup checkpoints = %v, want initial status and heartbeat",
			startupCheckpoints,
		)
	}
	for index := 1; index < len(startupCheckpoints); index++ {
		if startupCheckpoints[index] <= startupCheckpoints[index-1] {
			t.Fatalf("startup checkpoints did not advance: %v", startupCheckpoints)
		}
	}
	if len(stopCheckpoints) < 2 {
		t.Fatalf(
			"shutdown checkpoints = %v, want initial status and heartbeat",
			stopCheckpoints,
		)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("startup timeout did not cancel the service context")
	}
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "startup timed out") ||
		!strings.Contains(joinedLogs, "shutdown timed out") {
		t.Fatalf("startup timeout logs = %q", joinedLogs)
	}
}

func TestWindowsQuickServiceDeadlinesFitDesktopBrokerWindow(t *testing.T) {
	const resultDeliveryMargin = 5 * time.Second
	lifecycle := defaultWindowsServiceStartupTimeout +
		defaultWindowsServiceShutdownTimeout +
		defaultWindowsRuntimeCleanupTimeout
	if lifecycle > windowsDesktopOperationTimeout-resultDeliveryMargin {
		t.Fatalf(
			"failed-start lifecycle budget %s exceeds desktop broker budget %s minus result margin %s",
			lifecycle, windowsDesktopOperationTimeout, resultDeliveryMargin,
		)
	}
}

func TestWindowsQuickServiceFailsBeforeReady(t *testing.T) {
	changes := make(chan svc.Status, 2)
	service := &windowsQuickService{
		run: func(context.Context, func(), func(string)) error {
			return errors.New("startup failed")
		},
	}
	_, code := service.Execute(nil, make(chan svc.ChangeRequest), changes)
	if code != 1 {
		t.Fatalf("failed service exit code = %d, want 1", code)
	}
	expectWindowsServiceState(t, changes, svc.StartPending)
	select {
	case status := <-changes:
		t.Fatalf("handler reported final state %v instead of returning it to SCM", status.State)
	default:
	}
}

func TestWindowsQuickServiceShutdownTimesOutWithProgress(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 32)
	release := make(chan struct{})
	defer close(release)
	var logs []string
	service := &windowsQuickService{
		shutdownTimeout:   60 * time.Millisecond,
		heartbeatInterval: 10 * time.Millisecond,
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
		run: func(
			_ context.Context,
			ready func(),
			progress func(string),
		) error {
			ready()
			progress(shutdownStageNetwork)
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
				t.Fatalf("shutdown state = %v, want StopPending", status.State)
			}
			if status.WaitHint != uint32(windowsServiceWaitHint/time.Millisecond) {
				t.Fatalf("shutdown wait hint = %d", status.WaitHint)
			}
			checkpoints = append(checkpoints, status.CheckPoint)
		case code := <-result:
			if code != 1 {
				t.Fatalf("timed-out service exit code = %d, want 1", code)
			}
			collecting = false
		case <-time.After(time.Second):
			t.Fatal("service shutdown timeout did not fire")
		}
	}

	if len(checkpoints) < 2 {
		t.Fatalf("shutdown checkpoints = %v, want heartbeat progress", checkpoints)
	}
	for index := 1; index < len(checkpoints); index++ {
		if checkpoints[index] < checkpoints[index-1] {
			t.Fatalf("shutdown checkpoints regressed: %v", checkpoints)
		}
	}
	select {
	case status := <-changes:
		t.Fatalf("handler reported final state %v instead of returning it to SCM", status.State)
	default:
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "timed out") ||
		!strings.Contains(got, shutdownStageNetwork) {
		t.Fatalf("shutdown timeout log = %q", got)
	}
}

func TestWindowsShutdownCheckpointDescribesCurrentStage(t *testing.T) {
	for _, stage := range []string{
		shutdownStageStopRefresh,
		shutdownStagePreDown,
		shutdownStageNetwork,
		shutdownStagePostDown,
		shutdownStageEndpoint,
		shutdownStageCore,
		shutdownStageComplete,
	} {
		checkpoint := windowsShutdownStageIndex(stage) * 100
		if got := windowsShutdownStageFromCheckpoint(checkpoint); got != stage {
			t.Fatalf("checkpoint %d stage = %q, want %q", checkpoint, got, stage)
		}
	}
}

func TestWindowsServiceWaitErrorIncludesShutdownDiagnostics(t *testing.T) {
	status := svc.Status{
		State:      svc.StopPending,
		CheckPoint: windowsShutdownStageIndex(shutdownStageNetwork)*100 + 5,
		WaitHint:   5000,
	}
	err := windowsServiceWaitError(
		context.DeadlineExceeded,
		windowsServiceName("wg0"),
		svc.Stopped,
		status,
		30*time.Second,
	)
	message := err.Error()
	for _, want := range []string{
		"remained StopPending for 30s",
		"last checkpoint=405",
		"wait hint=5000ms",
		shutdownStageNetwork,
		"endpoint lease cleanup may still be pending",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("wait error %q does not contain %q", message, want)
		}
	}
}

func TestWindowsServiceStoppedErrorSuggestsExplicitRepair(t *testing.T) {
	pending := svc.Status{
		State:      svc.StopPending,
		CheckPoint: windowsShutdownStageIndex(shutdownStageEndpoint) * 100,
		WaitHint:   5000,
	}
	stopped := svc.Status{State: svc.Stopped, Win32ExitCode: 1}
	message := windowsServiceStoppedError(
		windowsServiceName("wg0"), pending, stopped, 25*time.Second,
	).Error()
	for _, want := range []string{
		"shutdown failure",
		"win32_exit=1",
		shutdownStageEndpoint,
		"down wg0 --repair",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("stopped error %q does not contain %q", message, want)
		}
	}
}

func TestWindowsServiceNameIsNamespaced(t *testing.T) {
	if got, want := windowsServiceName("wg0"), "wg-quic-quick@wg0"; got != want {
		t.Fatalf("windowsServiceName(wg0) = %q, want %q", got, want)
	}
}

func expectWindowsServiceState(t *testing.T, changes <-chan svc.Status, want svc.State) {
	t.Helper()
	status := expectWindowsServiceStatus(t, changes)
	if status.State != want {
		t.Fatalf("service state = %v, want %v", status.State, want)
	}
}

func expectWindowsServiceStatus(t *testing.T, changes <-chan svc.Status) svc.Status {
	t.Helper()
	select {
	case status := <-changes:
		return status
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Windows service status")
		return svc.Status{}
	}
}
