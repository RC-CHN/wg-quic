//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/RC-CHN/wg-quic/internal/platform"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const windowsServicePrefix = "wg-quic-quick@"

func Manage(ctx context.Context, action, name string) error {
	if err := platform.Current().ValidateInterfaceName(name); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	switch action {
	case "up":
		if err := Check(name); err != nil {
			return err
		}
		return startWindowsService(ctx, name)
	case "down":
		return stopWindowsService(ctx, name)
	default:
		return fmt.Errorf("unknown management action %q", action)
	}
}

func startWindowsService(ctx context.Context, name string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	serviceName := windowsServiceName(name)
	service, err := manager.OpenService(serviceName)
	if err == nil {
		status, queryErr := queryWindowsServiceStatus(service)
		if queryErr != nil {
			service.Close()
			return queryErr
		}
		if status.State != svc.Stopped {
			service.Close()
			return fmt.Errorf("Windows service %s is already active", serviceName)
		}
		if deleteErr := service.Delete(); deleteErr != nil && !errors.Is(deleteErr, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			service.Close()
			return deleteErr
		}
		service.Close()
		if err := waitWindowsServiceDeleted(ctx, manager, serviceName); err != nil {
			return err
		}
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	service, err = manager.CreateService(serviceName, executable, mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:    mgr.StartManual,
		ErrorControl: mgr.ErrorNormal,
		Dependencies: []string{"Nsi", "TcpIp"},
		DisplayName:  "wg-quic quick tunnel: " + name,
		SidType:      windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "run", name)
	if err != nil {
		return fmt.Errorf("create Windows service %s: %w", serviceName, err)
	}
	defer service.Close()
	if err := service.Start(); err != nil {
		_ = service.Delete()
		return fmt.Errorf("start Windows service %s: %w", serviceName, err)
	}
	if err := waitWindowsServiceState(
		ctx, service, serviceName, svc.Running,
	); err != nil {
		_ = service.Delete()
		return err
	}
	return nil
}

func stopWindowsService(ctx context.Context, name string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	serviceName := windowsServiceName(name)
	service, err := manager.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open Windows service %s: %w", serviceName, err)
	}
	defer service.Close()
	status, err := queryWindowsServiceStatus(service)
	if err != nil {
		return err
	}
	if status.State == svc.Stopped &&
		(status.Win32ExitCode != 0 ||
			status.ServiceSpecificExitCode != 0) {
		return windowsServiceStoppedError(
			serviceName, status, status, 0,
		)
	}
	if status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop Windows service %s: %w", serviceName, err)
		}
		if err := waitWindowsServiceState(
			ctx, service, serviceName, svc.Stopped,
		); err != nil {
			return err
		}
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("delete Windows service %s: %w", serviceName, err)
	}
	return nil
}

func waitWindowsServiceState(
	ctx context.Context,
	service *mgr.Service,
	serviceName string,
	want svc.State,
) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	started := time.Now()
	var last svc.Status
	for {
		status, err := queryWindowsServiceStatus(service)
		if err != nil {
			return err
		}
		previous := last
		last = status
		if status.State == want {
			if want == svc.Stopped &&
				(status.Win32ExitCode != 0 ||
					status.ServiceSpecificExitCode != 0) {
				if previous.State != svc.StopPending {
					previous = status
				}
				return windowsServiceStoppedError(
					serviceName, previous, status,
					time.Since(started),
				)
			}
			return nil
		}
		if status.State == svc.Stopped && want != svc.Stopped {
			return fmt.Errorf(
				"Windows service %s stopped before reaching state %s: win32_exit=%d service_exit=%d",
				serviceName, windowsServiceStateName(want),
				status.Win32ExitCode, status.ServiceSpecificExitCode,
			)
		}
		select {
		case <-ctx.Done():
			return windowsServiceWaitError(
				ctx.Err(), serviceName, want, last, time.Since(started),
			)
		case <-ticker.C:
		}
	}
}

func windowsServiceStoppedError(
	serviceName string,
	lastPending svc.Status,
	stopped svc.Status,
	elapsed time.Duration,
) error {
	elapsed = elapsed.Round(100 * time.Millisecond)
	stage := windowsShutdownStageFromCheckpoint(lastPending.CheckPoint)
	return fmt.Errorf(
		"Windows service %s stopped with a shutdown failure after %s: win32_exit=%d service_exit=%d last checkpoint=%d wait hint=%dms current cleanup stage=%q; endpoint lease cleanup may still be pending; run `wg-quic-quick down %s --repair` for explicit recovery",
		serviceName, elapsed, stopped.Win32ExitCode,
		stopped.ServiceSpecificExitCode, lastPending.CheckPoint,
		lastPending.WaitHint, stage,
		strings.TrimPrefix(serviceName, windowsServicePrefix),
	)
}

func waitWindowsServiceDeleted(ctx context.Context, manager *mgr.Mgr, name string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		service, err := manager.OpenService(name)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		if err == nil {
			service.Close()
		} else if !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"Windows service %s remained marked for deletion: %w",
				name, ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func queryWindowsServiceStatus(service *mgr.Service) (svc.Status, error) {
	var native windows.SERVICE_STATUS_PROCESS
	var needed uint32
	err := windows.QueryServiceStatusEx(
		service.Handle,
		windows.SC_STATUS_PROCESS_INFO,
		(*byte)(unsafe.Pointer(&native)),
		uint32(unsafe.Sizeof(native)),
		&needed,
	)
	if err != nil {
		return svc.Status{}, err
	}
	return svc.Status{
		State:                   svc.State(native.CurrentState),
		Accepts:                 svc.Accepted(native.ControlsAccepted),
		CheckPoint:              native.CheckPoint,
		WaitHint:                native.WaitHint,
		ProcessId:               native.ProcessId,
		Win32ExitCode:           native.Win32ExitCode,
		ServiceSpecificExitCode: native.ServiceSpecificExitCode,
	}, nil
}

func windowsServiceWaitError(
	cause error,
	serviceName string,
	want svc.State,
	status svc.Status,
	elapsed time.Duration,
) error {
	elapsed = elapsed.Round(100 * time.Millisecond)
	if status.State == svc.StopPending && want == svc.Stopped {
		stage := windowsShutdownStageFromCheckpoint(status.CheckPoint)
		return fmt.Errorf(
			"Windows service %s remained StopPending for %s: last checkpoint=%d wait hint=%dms current cleanup stage=%q; endpoint lease cleanup may still be pending: %w",
			serviceName, elapsed, status.CheckPoint, status.WaitHint, stage, cause,
		)
	}
	return fmt.Errorf(
		"Windows service %s remained %s for %s while waiting for %s: last checkpoint=%d wait hint=%dms: %w",
		serviceName, windowsServiceStateName(status.State), elapsed,
		windowsServiceStateName(want), status.CheckPoint, status.WaitHint, cause,
	)
}

func windowsServiceStateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "StartPending"
	case svc.StopPending:
		return "StopPending"
	case svc.Running:
		return "Running"
	case svc.ContinuePending:
		return "ContinuePending"
	case svc.PausePending:
		return "PausePending"
	case svc.Paused:
		return "Paused"
	default:
		return fmt.Sprintf("state(%d)", state)
	}
}

func windowsServiceName(name string) string {
	return windowsServicePrefix + name
}
