//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

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
		status, queryErr := service.Query()
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
	if err := waitWindowsServiceState(ctx, service, svc.Running); err != nil {
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
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop Windows service %s: %w", serviceName, err)
		}
		if err := waitWindowsServiceState(ctx, service, svc.Stopped); err != nil {
			return err
		}
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("delete Windows service %s: %w", serviceName, err)
	}
	return nil
}

func waitWindowsServiceState(ctx context.Context, service *mgr.Service, want svc.State) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == want {
			return nil
		}
		if status.State == svc.Stopped && want != svc.Stopped {
			return fmt.Errorf("Windows service stopped before reaching state %d", want)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func windowsServiceName(name string) string {
	return windowsServicePrefix + name
}
