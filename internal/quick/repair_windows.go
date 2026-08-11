//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	windowsRepairTimeout   = 60 * time.Second
	windowsRepairStopGrace = 30 * time.Second
	windowsRepairForceWait = 10 * time.Second
)

// Repair stops a Windows tunnel service, forcibly terminating only the exact
// service process if the explicit repair grace period expires, then reconciles
// recoverable Wintun and endpoint-route state for the named tunnel.
func Repair(ctx context.Context, name string) (RepairResult, error) {
	var result RepairResult
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return result, err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, windowsRepairTimeout)
		defer cancel()
	}

	forced, err := repairWindowsService(ctx, name)
	result.ForcedServiceTermination = forced
	if err != nil {
		return result, err
	}
	if status, err := control.Read(host.ControlPath(name)); err == nil {
		return result, fmt.Errorf(
			"wg-quic core for tunnel %q is still active in state %q; refusing to mutate its adapter or route state",
			name, status.State,
		)
	} else if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) &&
		!errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return result, fmt.Errorf(
			"verify wg-quic core for tunnel %q is stopped before repair: %w",
			name, err,
		)
	}

	path := host.ConfigPath(name)
	cfg, configErr := config.ParseFile(path)
	if errors.Is(configErr, os.ErrNotExist) {
		configErr = nil
		cfg = nil
	}
	repairErr := platform.RepairWindows(ctx, name, cfg)
	if configErr != nil {
		configErr = fmt.Errorf(
			"parse %s for residual network policy cleanup: %w",
			path, configErr,
		)
	}
	return result, errors.Join(configErr, repairErr)
}

func repairWindowsService(ctx context.Context, name string) (bool, error) {
	forced := false
	manager, err := mgr.Connect()
	if err != nil {
		return forced, fmt.Errorf("connect to Windows Service Control Manager for repair: %w", err)
	}
	defer manager.Disconnect()

	serviceName := windowsServiceName(name)
	service, err := manager.OpenService(serviceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return forced, nil
	}
	if err != nil {
		return forced, fmt.Errorf("open Windows service %s for repair: %w", serviceName, err)
	}
	lifecycleManager := windowsNativeServiceLifecycleManager{manager: manager}
	runtimeExecutable := windowsTrustedRuntimeExecutableFromService(
		windowsNativeServiceLifecycleService{service: service},
	)

	status, err := queryWindowsServiceStatus(service)
	if err != nil {
		service.Close()
		return forced, fmt.Errorf("query Windows service %s for repair: %w", serviceName, err)
	}
	if status.State != svc.Stopped {
		if status.State != svc.StopPending {
			if _, stopErr := service.Control(svc.Stop); stopErr != nil &&
				!errors.Is(stopErr, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) &&
				!errors.Is(stopErr, windows.ERROR_SERVICE_NOT_ACTIVE) {
				service.Close()
				return forced, fmt.Errorf(
					"request repair stop for Windows service %s: %w",
					serviceName, stopErr,
				)
			}
		}

		graceCtx, cancelGrace := context.WithTimeout(
			ctx, windowsRepairStopGrace,
		)
		waitErr := waitWindowsServiceState(
			graceCtx, service, serviceName, svc.Stopped,
		)
		cancelGrace()
		if waitErr != nil {
			if err := ctx.Err(); err != nil {
				service.Close()
				return forced, errors.Join(err, waitErr)
			}
			status, err = queryWindowsServiceStatus(service)
			if err != nil {
				service.Close()
				return forced, errors.Join(waitErr, err)
			}
			if status.State != svc.Stopped {
				if err := terminateWindowsServiceProcess(
					serviceName, status,
				); err != nil {
					service.Close()
					return forced, errors.Join(waitErr, err)
				}
				forced = true
				forceCtx, cancelForce := context.WithTimeout(
					ctx, windowsRepairForceWait,
				)
				err = waitWindowsServiceState(
					forceCtx, service, serviceName, svc.Stopped,
				)
				cancelForce()
				if err != nil {
					service.Close()
					return forced, fmt.Errorf(
						"wait for forcibly terminated Windows service %s: %w",
						serviceName, err,
					)
				}
			}
		}
	}

	deleteErr := service.Delete()
	if deleteErr != nil &&
		!errors.Is(deleteErr, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		service.Close()
		return forced, fmt.Errorf("delete repaired Windows service %s: %w", serviceName, deleteErr)
	}
	if err := service.Close(); err != nil {
		return forced, fmt.Errorf("close repaired Windows service %s: %w", serviceName, err)
	}
	if err := waitWindowsServiceDeleted(ctx, manager, serviceName); err != nil {
		return forced, err
	}
	cleanupWindowsRuntimeBestEffort(
		lifecycleManager,
		runtimeExecutable,
		defaultWindowsRuntimeLifecycleOperations(),
	)
	return forced, nil
}

func terminateWindowsServiceProcess(
	serviceName string,
	status svc.Status,
) error {
	if status.ProcessId == 0 {
		return fmt.Errorf(
			"Windows service %s remained %s with no process ID; refusing repair termination",
			serviceName, windowsServiceStateName(status.State),
		)
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE,
		false,
		status.ProcessId,
	)
	if err != nil {
		return fmt.Errorf(
			"open stuck Windows service %s process %d for explicit repair: %w",
			serviceName, status.ProcessId, err,
		)
	}
	defer windows.CloseHandle(process)
	if err := windows.TerminateProcess(process, 1); err != nil {
		return fmt.Errorf(
			"terminate stuck Windows service %s process %d during explicit repair: %w",
			serviceName, status.ProcessId, err,
		)
	}
	return nil
}
