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
	expectWindowsServiceState(t, changes, svc.Stopped)
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("clean service stop exit code = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Windows service did not stop")
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
	expectWindowsServiceState(t, changes, svc.Stopped)
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
	for {
		select {
		case status := <-changes:
			if status.State == svc.Stopped {
				goto stopped
			}
			if status.State != svc.StopPending {
				t.Fatalf("shutdown state = %v, want StopPending", status.State)
			}
			if status.WaitHint != uint32(windowsServiceWaitHint/time.Millisecond) {
				t.Fatalf("shutdown wait hint = %d", status.WaitHint)
			}
			checkpoints = append(checkpoints, status.CheckPoint)
		case <-time.After(time.Second):
			t.Fatal("service shutdown timeout did not fire")
		}
	}

stopped:
	if len(checkpoints) < 2 {
		t.Fatalf("shutdown checkpoints = %v, want heartbeat progress", checkpoints)
	}
	for index := 1; index < len(checkpoints); index++ {
		if checkpoints[index] < checkpoints[index-1] {
			t.Fatalf("shutdown checkpoints regressed: %v", checkpoints)
		}
	}
	select {
	case code := <-result:
		if code != 1 {
			t.Fatalf("timed-out service exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out service handler did not return")
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
	select {
	case status := <-changes:
		if status.State != want {
			t.Fatalf("service state = %v, want %v", status.State, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for service state %v", want)
	}
}
