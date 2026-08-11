//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const defaultWindowsRuntimeCleanupTimeout = 15 * time.Second

var (
	windowsRuntimeCleanupTimeout    = defaultWindowsRuntimeCleanupTimeout
	windowsRuntimeCurrentExecutable = os.Executable
)

type windowsServiceLifecycleService interface {
	status() (svc.Status, error)
	config() (mgr.Config, error)
	start(...string) error
	control(svc.Cmd) (svc.Status, error)
	delete() error
	close() error
}

type windowsServiceLifecycleManager interface {
	openService(string) (windowsServiceLifecycleService, error)
	createService(
		string,
		string,
		mgr.Config,
		...string,
	) (windowsServiceLifecycleService, error)
	listServices() ([]string, error)
}

type windowsNativeServiceLifecycleManager struct {
	manager *mgr.Mgr
}

func (m windowsNativeServiceLifecycleManager) openService(
	name string,
) (windowsServiceLifecycleService, error) {
	service, err := m.manager.OpenService(name)
	if err != nil {
		return nil, err
	}
	return windowsNativeServiceLifecycleService{service: service}, nil
}

func (m windowsNativeServiceLifecycleManager) createService(
	name string,
	executable string,
	configuration mgr.Config,
	arguments ...string,
) (windowsServiceLifecycleService, error) {
	service, err := m.manager.CreateService(
		name,
		executable,
		configuration,
		arguments...,
	)
	if err != nil {
		return nil, err
	}
	return windowsNativeServiceLifecycleService{service: service}, nil
}

func (m windowsNativeServiceLifecycleManager) listServices() ([]string, error) {
	return m.manager.ListServices()
}

type windowsNativeServiceLifecycleService struct {
	service *mgr.Service
}

func (s windowsNativeServiceLifecycleService) status() (svc.Status, error) {
	return queryWindowsServiceStatus(s.service)
}

func (s windowsNativeServiceLifecycleService) config() (mgr.Config, error) {
	return s.service.Config()
}

func (s windowsNativeServiceLifecycleService) start(
	arguments ...string,
) error {
	return s.service.Start(arguments...)
}

func (s windowsNativeServiceLifecycleService) control(
	command svc.Cmd,
) (svc.Status, error) {
	return s.service.Control(command)
}

func (s windowsNativeServiceLifecycleService) delete() error {
	return s.service.Delete()
}

func (s windowsNativeServiceLifecycleService) close() error {
	return s.service.Close()
}

type windowsRuntimeStage struct {
	executable string
	lease      *windowsSecurePathLease
}

type windowsRuntimeLifecycleOperations struct {
	stage    func() (windowsRuntimeStage, error)
	capture  func(windowsServiceLifecycleService) string
	diagnose func(string) (string, error)
	cleanup  func(
		context.Context,
		windowsServiceLifecycleManager,
		string,
	) error
}

func defaultWindowsRuntimeLifecycleOperations() windowsRuntimeLifecycleOperations {
	return windowsRuntimeLifecycleOperations{
		stage: func() (windowsRuntimeStage, error) {
			sourceExecutable, err := os.Executable()
			if err != nil {
				return windowsRuntimeStage{}, err
			}
			executable, lease, err := prepareWindowsServiceRuntime(
				sourceExecutable,
			)
			return windowsRuntimeStage{
				executable: executable,
				lease:      lease,
			}, err
		},
		capture:  windowsTrustedRuntimeExecutableFromService,
		diagnose: readWindowsServiceFailureRecord,
		cleanup:  cleanupWindowsServiceRuntime,
	}
}

func windowsTrustedRuntimeExecutableFromService(
	service windowsServiceLifecycleService,
) string {
	configuration, err := service.config()
	if err != nil {
		return ""
	}
	arguments, err := windows.DecomposeCommandLine(
		configuration.BinaryPathName,
	)
	if err != nil || len(arguments) == 0 {
		return ""
	}
	if _, err := windowsRuntimeDirectoryFromExecutablePath(
		arguments[0],
		"wg-quic-quick.exe",
	); err != nil {
		return ""
	}
	return arguments[0]
}

func cleanupWindowsRuntimeBestEffort(
	manager windowsServiceLifecycleManager,
	executable string,
	operation windowsRuntimeLifecycleOperations,
) {
	if executable == "" || operation.cleanup == nil {
		return
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		windowsRuntimeCleanupTimeout,
	)
	defer cancel()
	if err := operation.cleanup(ctx, manager, executable); err != nil {
		log.Printf(
			"retain Windows service runtime %q after safe cleanup failed: %v",
			executable,
			err,
		)
	}
}

func closeWindowsRuntimeStage(stage *windowsRuntimeStage) error {
	if stage == nil || stage.lease == nil {
		return nil
	}
	err := stage.lease.Close()
	stage.lease = nil
	return err
}

func rollbackWindowsStartedService(
	manager windowsServiceLifecycleManager,
	service windowsServiceLifecycleService,
	serviceName string,
	stage *windowsRuntimeStage,
	operation windowsRuntimeLifecycleOperations,
) error {
	rollbackContext, cancel := context.WithTimeout(
		context.Background(),
		windowsRuntimeCleanupTimeout,
	)
	defer cancel()

	var rollbackErrors []error
	status, statusErr := service.status()
	if statusErr != nil || status.State != svc.Stopped {
		if statusErr != nil || status.State != svc.StopPending {
			if _, stopErr := service.control(svc.Stop); stopErr != nil &&
				!errors.Is(stopErr, windows.ERROR_SERVICE_NOT_ACTIVE) &&
				!errors.Is(stopErr, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"request rollback stop for Windows service %s: %w",
					serviceName,
					stopErr,
				))
			}
		}
		if waitErr := waitWindowsLifecycleServiceState(
			rollbackContext,
			service,
			serviceName,
			svc.Stopped,
		); waitErr != nil {
			rollbackErrors = append(rollbackErrors, waitErr)
		}
	}
	deleteAccepted := true
	if deleteErr := service.delete(); deleteErr != nil &&
		!errors.Is(deleteErr, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		deleteAccepted = false
		rollbackErrors = append(rollbackErrors, fmt.Errorf(
			"delete failed Windows service %s during rollback: %w",
			serviceName,
			deleteErr,
		))
	}
	if closeErr := service.close(); closeErr != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf(
			"close failed Windows service %s during rollback: %w",
			serviceName,
			closeErr,
		))
	}
	deleted := false
	if deleteAccepted {
		if waitErr := waitWindowsLifecycleServiceDeleted(
			rollbackContext,
			manager,
			serviceName,
		); waitErr != nil {
			rollbackErrors = append(rollbackErrors, waitErr)
		} else {
			deleted = true
		}
	}
	if closeErr := closeWindowsRuntimeStage(stage); closeErr != nil {
		rollbackErrors = append(rollbackErrors, closeErr)
	}
	if deleted {
		cleanupWindowsRuntimeBestEffort(
			manager,
			stage.executable,
			operation,
		)
	}
	return errors.Join(rollbackErrors...)
}

func waitWindowsLifecycleServiceState(
	ctx context.Context,
	service windowsServiceLifecycleService,
	serviceName string,
	want svc.State,
) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	started := time.Now()
	var last svc.Status
	for {
		status, err := service.status()
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
					serviceName,
					previous,
					status,
					time.Since(started),
				)
			}
			return nil
		}
		if status.State == svc.Stopped && want != svc.Stopped {
			return fmt.Errorf(
				"Windows service %s stopped before reaching state %s: win32_exit=%d service_exit=%d",
				serviceName,
				windowsServiceStateName(want),
				status.Win32ExitCode,
				status.ServiceSpecificExitCode,
			)
		}
		select {
		case <-ctx.Done():
			return windowsServiceWaitError(
				ctx.Err(),
				serviceName,
				want,
				last,
				time.Since(started),
			)
		case <-ticker.C:
		}
	}
}

func waitWindowsLifecycleServiceDeleted(
	ctx context.Context,
	manager windowsServiceLifecycleManager,
	name string,
) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		service, err := manager.openService(name)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		if err == nil {
			_ = service.close()
		} else if !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"Windows service %s remained marked for deletion: %w",
				name,
				ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func cleanupWindowsServiceRuntime(
	ctx context.Context,
	manager windowsServiceLifecycleManager,
	executable string,
) error {
	directory, err := windowsRuntimeDirectoryFromExecutablePath(
		executable,
		"wg-quic-quick.exe",
	)
	if err != nil {
		return fmt.Errorf("resolve trusted Windows service runtime: %w", err)
	}
	return cleanupWindowsRuntimeDirectory(ctx, manager, directory)
}

func cleanupWindowsRuntimeDirectory(
	ctx context.Context,
	manager windowsServiceLifecycleManager,
	directory string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateWindowsTrustedRuntimeDirectory(directory); err != nil {
		return err
	}
	current, err := windowsRuntimeDirectoryIsCurrentProcess(directory)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	references, err := windowsReferencedRuntimeDirectories(manager)
	if err != nil {
		return err
	}
	if _, referenced := references[windowsPathKey(directory)]; referenced {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return removeWindowsTrustedRuntimeDirectoryWithRetry(
		ctx,
		directory,
		removeWindowsTrustedRuntimeDirectory,
	)
}

func removeWindowsTrustedRuntimeDirectoryWithRetry(
	ctx context.Context,
	directory string,
	remove func(string) error,
) error {
	for {
		err := remove(directory)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) &&
			!errors.Is(err, windows.ERROR_LOCK_VIOLATION) &&
			!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
	}
}

func windowsReferencedRuntimeDirectories(
	manager windowsServiceLifecycleManager,
) (map[string]struct{}, error) {
	names, err := manager.listServices()
	if err != nil {
		return nil, fmt.Errorf("list Windows services before runtime cleanup: %w", err)
	}
	references := make(map[string]struct{})
	for _, name := range names {
		if !strings.HasPrefix(name, windowsServicePrefix) {
			continue
		}
		service, err := manager.openService(name)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf(
				"open Windows service %s before runtime cleanup: %w",
				name,
				err,
			)
		}
		configuration, configErr := service.config()
		closeErr := service.close()
		if configErr != nil {
			return nil, errors.Join(
				fmt.Errorf(
					"inspect Windows service %s before runtime cleanup: %w",
					name,
					configErr,
				),
				closeErr,
			)
		}
		if closeErr != nil {
			return nil, fmt.Errorf(
				"close Windows service %s before runtime cleanup: %w",
				name,
				closeErr,
			)
		}
		arguments, err := windows.DecomposeCommandLine(
			configuration.BinaryPathName,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"parse Windows service %s command before runtime cleanup: %w",
				name,
				err,
			)
		}
		if len(arguments) == 0 {
			return nil, fmt.Errorf(
				"parse Windows service %s command before runtime cleanup: empty command",
				name,
			)
		}
		directory, err := windowsRuntimeDirectoryFromExecutablePath(
			arguments[0],
			"wg-quic-quick.exe",
		)
		if err == nil {
			references[windowsPathKey(directory)] = struct{}{}
			continue
		}
		if validateWindowsTrustedInstalledFile(arguments[0]) == nil {
			continue
		}
		return nil, fmt.Errorf(
			"Windows service %s has an unrecognized executable path %q; refusing runtime cleanup",
			name,
			arguments[0],
		)
	}
	return references, nil
}

func removeWindowsTrustedRuntimeDirectory(directory string) error {
	if err := validateWindowsTrustedRuntimeDirectory(directory); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("enumerate trusted Windows runtime %q: %w", directory, err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !windowsRuntimeCleanupEntryAllowed(entry.Name()) {
			return fmt.Errorf(
				"trusted Windows runtime %q contains non-whitelisted entry %q",
				directory,
				entry.Name(),
			)
		}
		path := filepath.Join(directory, entry.Name())
		if err := validateWindowsTrustedRuntimeExecutable(
			path,
			entry.Name(),
		); err != nil {
			return fmt.Errorf(
				"validate Windows runtime cleanup entry %q: %w",
				entry.Name(),
				err,
			)
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		if err := deleteWindowsTrustedRuntimePath(
			path,
			false,
			true,
			windowsStrictFileSDDL,
		); err != nil {
			return fmt.Errorf("remove trusted Windows runtime file %q: %w", path, err)
		}
	}
	if err := deleteWindowsTrustedRuntimePath(
		directory,
		true,
		false,
		windowsStrictDirectorySDDL,
	); err != nil {
		return fmt.Errorf("remove trusted Windows runtime directory %q: %w", directory, err)
	}
	return nil
}

func deleteWindowsTrustedRuntimePath(
	path string,
	wantDirectory bool,
	requireSingleLink bool,
	sddl string,
) error {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	handle, err := openWindowsFileNoFollow(
		path,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|
			windows.FILE_SHARE_WRITE|
			windows.FILE_SHARE_DELETE,
	)
	if err != nil {
		return err
	}
	if err := inspectWindowsPathHandle(
		handle,
		path,
		wantDirectory,
		requireSingleLink,
	); err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}
	if err := verifyWindowsPathHandleSecurity(
		handle,
		descriptor,
		"runtime cleanup target",
	); err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}
	deleteFile := byte(1)
	setErr := windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfo,
		&deleteFile,
		1,
	)
	closeErr := windows.CloseHandle(handle)
	return errors.Join(setErr, closeErr)
}

func sweepWindowsOrphanRuntimes(ctx context.Context) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows Service Control Manager for runtime sweep: %w", err)
	}
	defer manager.Disconnect()
	lifecycleManager := windowsNativeServiceLifecycleManager{manager: manager}
	// Layout initialization intentionally starts with interfaces. If an older
	// untrusted product root must be quarantined, opening runtime first would
	// consume the one-shot migration object without copying its hook-free
	// configurations into the new protected interfaces directory.
	if err := ensureWindowsProgramDataLayout(); err != nil {
		return fmt.Errorf(
			"secure Windows ProgramData before runtime sweep: %w",
			err,
		)
	}
	runtimeRoot, lease, err := openWindowsSecureRuntimeRoot()
	if err != nil {
		return err
	}
	entries, readErr := os.ReadDir(runtimeRoot)
	closeErr := lease.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join(runtimeRoot, entry.Name())
		if _, _, _, err := windowsTrustedRuntimeDirectoryLayout(
			directory,
		); err != nil {
			continue
		}
		if err := cleanupWindowsRuntimeDirectory(
			ctx,
			lifecycleManager,
			directory,
		); err != nil {
			log.Printf(
				"retain orphan Windows service runtime %q: %v",
				directory,
				err,
			)
		}
	}
	return nil
}

func sweepWindowsOrphanRuntimesBestEffort(ctx context.Context) {
	sweepContext, cancel := context.WithTimeout(
		ctx,
		windowsRuntimeCleanupTimeout,
	)
	defer cancel()
	if err := sweepWindowsOrphanRuntimes(sweepContext); err != nil {
		log.Printf("skip Windows service runtime orphan sweep: %v", err)
	}
}

func windowsPathsEqual(left string, right string) bool {
	return windowsPathKey(left) == windowsPathKey(right)
}

func windowsRuntimeDirectoryIsCurrent(
	directory string,
	currentExecutable string,
) bool {
	return windowsPathsEqual(filepath.Dir(currentExecutable), directory)
}

func windowsRuntimeDirectoryIsCurrentProcess(
	directory string,
) (bool, error) {
	current, err := windowsRuntimeCurrentExecutable()
	if err != nil {
		return false, fmt.Errorf(
			"resolve current executable before Windows runtime cleanup: %w",
			err,
		)
	}
	return windowsRuntimeDirectoryIsCurrent(directory, current), nil
}

func windowsRuntimeCleanupEntryAllowed(name string) bool {
	switch name {
	case "wg-quic-quick.exe", "wg-quic.exe", "wintun.dll",
		windowsServiceFailureFileName:
		return true
	default:
		return false
	}
}

func windowsPathKey(path string) string {
	path = filepath.Clean(path)
	if strings.HasPrefix(path, `\\?\`) {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	return strings.ToLower(path)
}
