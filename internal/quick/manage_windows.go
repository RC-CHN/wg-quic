//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	return manageWindows(ctx, action, name, false)
}

func manageWindowsBroker(ctx context.Context, action, name string) error {
	return manageWindows(ctx, action, name, true)
}

func manageWindows(
	ctx context.Context,
	action,
	name string,
	brokerSafe bool,
) error {
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
		configLease, cfg, err := openAndValidateWindowsStoredConfig(name)
		if err != nil {
			return err
		}
		defer configLease.Close()
		if brokerSafe {
			if err := validateWindowsManagementHookFreeConfig(cfg); err != nil {
				return err
			}
		}
		return startWindowsService(ctx, name, brokerSafe)
	case "down":
		return stopWindowsService(ctx, name, brokerSafe)
	default:
		return fmt.Errorf("unknown management action %q", action)
	}
}

func startWindowsService(
	ctx context.Context,
	name string,
	brokerSafe bool,
) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	return startWindowsServiceManaged(
		ctx,
		windowsNativeServiceLifecycleManager{manager: manager},
		name,
		brokerSafe,
		defaultWindowsRuntimeLifecycleOperations(),
	)
}

func startWindowsServiceManaged(
	ctx context.Context,
	manager windowsServiceLifecycleManager,
	name string,
	brokerSafe bool,
	runtimeOperations windowsRuntimeLifecycleOperations,
) error {
	serviceName := windowsServiceName(name)
	service, err := manager.openService(serviceName)
	if err == nil {
		closed := false
		defer func() {
			if !closed {
				_ = service.close()
			}
		}()
		status, queryErr := service.status()
		if queryErr != nil {
			return queryErr
		}
		if status.State != svc.Stopped {
			if brokerSafe {
				safe, configErr := windowsServiceIsBrokerSafe(
					service, name,
				)
				if configErr != nil {
					return configErr
				}
				if !safe {
					return fmt.Errorf(
						"%w: active tunnel service %s was not started by the persistent management service",
						errWindowsManagementElevationRequired,
						serviceName,
					)
				}
			}
			return fmt.Errorf("Windows service %s is already active", serviceName)
		}
		oldRuntime := runtimeOperations.capture(service)
		if deleteErr := service.delete(); deleteErr != nil && !errors.Is(deleteErr, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return deleteErr
		}
		if err := service.close(); err != nil {
			return err
		}
		closed = true
		if err := waitWindowsLifecycleServiceDeleted(
			ctx,
			manager,
			serviceName,
		); err != nil {
			return err
		}
		cleanupWindowsRuntimeBestEffort(
			manager,
			oldRuntime,
			runtimeOperations,
		)
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	stage, err := runtimeOperations.stage()
	if err != nil {
		return fmt.Errorf("prepare Windows service runtime: %w", err)
	}
	serviceArguments := []string{"run", name}
	if brokerSafe {
		serviceArguments = append(serviceArguments, "--broker-safe")
	}
	service, err = manager.createService(serviceName, stage.executable, mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:    mgr.StartManual,
		ErrorControl: mgr.ErrorNormal,
		Dependencies: []string{"Nsi", "TcpIp"},
		DisplayName:  "wg-quic quick tunnel: " + name,
		SidType:      windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, serviceArguments...)
	if err != nil {
		closeErr := closeWindowsRuntimeStage(&stage)
		cleanupWindowsRuntimeBestEffort(
			manager,
			stage.executable,
			runtimeOperations,
		)
		return errors.Join(
			fmt.Errorf("create Windows service %s: %w", serviceName, err),
			closeErr,
		)
	}
	if err := service.start(); err != nil {
		rollbackErr := rollbackWindowsStartedService(
			manager,
			service,
			serviceName,
			&stage,
			runtimeOperations,
		)
		return errors.Join(
			fmt.Errorf("start Windows service %s: %w", serviceName, err),
			rollbackErr,
		)
	}
	if err := waitWindowsLifecycleServiceState(
		ctx, service, serviceName, svc.Running,
	); err != nil {
		rollbackErr := rollbackWindowsStartedService(
			manager,
			service,
			serviceName,
			&stage,
			runtimeOperations,
		)
		return errors.Join(err, rollbackErr)
	}
	return errors.Join(service.close(), closeWindowsRuntimeStage(&stage))
}

func stopWindowsService(
	ctx context.Context,
	name string,
	brokerSafe bool,
) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	return stopWindowsServiceManaged(
		ctx,
		windowsNativeServiceLifecycleManager{manager: manager},
		name,
		brokerSafe,
		defaultWindowsRuntimeLifecycleOperations(),
	)
}

func stopWindowsServiceManaged(
	ctx context.Context,
	manager windowsServiceLifecycleManager,
	name string,
	brokerSafe bool,
	runtimeOperations windowsRuntimeLifecycleOperations,
) error {
	serviceName := windowsServiceName(name)
	service, err := manager.openService(serviceName)
	if err != nil {
		return fmt.Errorf("open Windows service %s: %w", serviceName, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = service.close()
		}
	}()
	if brokerSafe {
		safe, err := windowsServiceIsBrokerSafe(service, name)
		if err != nil {
			return err
		}
		if !safe {
			return fmt.Errorf(
				"%w: tunnel service %s was not started by the persistent management service",
				errWindowsManagementElevationRequired,
				serviceName,
			)
		}
	}
	runtimeExecutable := runtimeOperations.capture(service)
	status, err := service.status()
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
		if _, err := service.control(svc.Stop); err != nil {
			return fmt.Errorf("stop Windows service %s: %w", serviceName, err)
		}
		if err := waitWindowsLifecycleServiceState(
			ctx, service, serviceName, svc.Stopped,
		); err != nil {
			return err
		}
	}
	if err := service.delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("delete Windows service %s: %w", serviceName, err)
	}
	if err := service.close(); err != nil {
		return fmt.Errorf("close Windows service %s: %w", serviceName, err)
	}
	closed = true
	if err := waitWindowsLifecycleServiceDeleted(
		ctx,
		manager,
		serviceName,
	); err != nil {
		return err
	}
	cleanupWindowsRuntimeBestEffort(
		manager,
		runtimeExecutable,
		runtimeOperations,
	)
	return nil
}

func windowsServiceIsBrokerSafe(
	service windowsServiceLifecycleService,
	name string,
) (bool, error) {
	configuration, err := service.config()
	if err != nil {
		return false, fmt.Errorf(
			"inspect Windows tunnel service command: %w", err,
		)
	}
	arguments, err := windows.DecomposeCommandLine(
		configuration.BinaryPathName,
	)
	if err != nil {
		return false, fmt.Errorf(
			"parse Windows tunnel service command: %w", err,
		)
	}
	return windowsServiceCommandIsBrokerSafe(arguments, name), nil
}

func windowsServiceCommandIsBrokerSafe(
	arguments []string,
	name string,
) bool {
	if !windowsServiceCommandShapeIsBrokerSafe(arguments, name) {
		return false
	}
	return validateWindowsTrustedRuntimeExecutable(
		arguments[0],
		"wg-quic-quick.exe",
	) == nil
}

func windowsServiceCommandShapeIsBrokerSafe(
	arguments []string,
	name string,
) bool {
	if len(arguments) != 4 ||
		!filepath.IsAbs(arguments[0]) ||
		arguments[1] != "run" ||
		arguments[2] != name ||
		arguments[3] != "--broker-safe" {
		return false
	}
	return true
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
