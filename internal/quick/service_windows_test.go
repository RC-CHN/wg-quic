//go:build windows

package quick

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsQuickServiceReportsReadyAndStopsCleanly(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 4)
	service := &windowsQuickService{
		run: func(ctx context.Context, ready func()) error {
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
		run: func(context.Context, func()) error {
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
