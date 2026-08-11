//go:build windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type fakeRuntimeLifecycleManager struct {
	services      map[string]*fakeRuntimeLifecycleService
	created       *fakeRuntimeLifecycleService
	createErr     error
	listErr       error
	openErrors    map[string]error
	createCalls   int
	createdConfig mgr.Config
	createdArgs   []string
}

func newFakeRuntimeLifecycleManager() *fakeRuntimeLifecycleManager {
	return &fakeRuntimeLifecycleManager{
		services:   make(map[string]*fakeRuntimeLifecycleService),
		openErrors: make(map[string]error),
	}
}

func (m *fakeRuntimeLifecycleManager) openService(
	name string,
) (windowsServiceLifecycleService, error) {
	if err := m.openErrors[name]; err != nil {
		return nil, err
	}
	service, ok := m.services[name]
	if !ok {
		return nil, windows.ERROR_SERVICE_DOES_NOT_EXIST
	}
	return service, nil
}

func (m *fakeRuntimeLifecycleManager) createService(
	name string,
	executable string,
	configuration mgr.Config,
	arguments ...string,
) (windowsServiceLifecycleService, error) {
	m.createCalls++
	if m.createErr != nil {
		return nil, m.createErr
	}
	service := m.created
	if service == nil {
		service = &fakeRuntimeLifecycleService{
			statuses: []svc.Status{{State: svc.Running}},
		}
	}
	service.manager = m
	service.name = name
	service.configuration = configuration
	service.configuration.BinaryPathName = executable
	m.services[name] = service
	m.createdConfig = configuration
	m.createdArgs = append([]string(nil), arguments...)
	return service, nil
}

func (m *fakeRuntimeLifecycleManager) listServices() ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	names := make([]string, 0, len(m.services))
	for name := range m.services {
		names = append(names, name)
	}
	return names, nil
}

type fakeRuntimeLifecycleService struct {
	manager       *fakeRuntimeLifecycleManager
	name          string
	configuration mgr.Config
	statuses      []svc.Status
	statusErr     error
	startErr      error
	controlErr    error
	controlErrors []error
	deleteErr     error
	closeErr      error
	startCalls    int
	stopCalls     int
	deleteCalls   int
	closeCalls    int
	deleted       bool
	retainDeleted bool
}

func (s *fakeRuntimeLifecycleService) status() (svc.Status, error) {
	if s.statusErr != nil {
		return svc.Status{}, s.statusErr
	}
	if len(s.statuses) == 0 {
		return svc.Status{State: svc.Stopped}, nil
	}
	status := s.statuses[0]
	if len(s.statuses) > 1 {
		s.statuses = s.statuses[1:]
	}
	return status, nil
}

func (s *fakeRuntimeLifecycleService) config() (mgr.Config, error) {
	return s.configuration, nil
}

func (s *fakeRuntimeLifecycleService) start(...string) error {
	s.startCalls++
	return s.startErr
}

func (s *fakeRuntimeLifecycleService) control(
	command svc.Cmd,
) (svc.Status, error) {
	controlErr := s.controlErr
	if command == svc.Stop {
		s.stopCalls++
		if len(s.controlErrors) != 0 {
			controlErr = s.controlErrors[0]
			s.controlErrors = s.controlErrors[1:]
		}
		if controlErr == nil {
			s.statuses = []svc.Status{{State: svc.Stopped}}
		}
	}
	return svc.Status{}, controlErr
}

func (s *fakeRuntimeLifecycleService) delete() error {
	s.deleteCalls++
	if s.deleteErr == nil || errors.Is(
		s.deleteErr,
		windows.ERROR_SERVICE_MARKED_FOR_DELETE,
	) {
		s.deleted = true
	}
	return s.deleteErr
}

func (s *fakeRuntimeLifecycleService) close() error {
	s.closeCalls++
	if s.deleted && !s.retainDeleted && s.manager != nil {
		delete(s.manager.services, s.name)
	}
	return s.closeErr
}

func fakeRuntimeOperations(
	stageExecutable string,
	cleaned *[]string,
) windowsRuntimeLifecycleOperations {
	return windowsRuntimeLifecycleOperations{
		stage: func() (windowsRuntimeStage, error) {
			return windowsRuntimeStage{executable: stageExecutable}, nil
		},
		capture: func(service windowsServiceLifecycleService) string {
			configuration, _ := service.config()
			return configuration.BinaryPathName
		},
		cleanup: func(
			_ context.Context,
			_ windowsServiceLifecycleManager,
			executable string,
		) error {
			*cleaned = append(*cleaned, executable)
			return nil
		},
	}
}

func TestWindowsRuntimeCleanupAfterNormalDown(t *testing.T) {
	manager := newFakeRuntimeLifecycleManager()
	const runtime = `C:\ProgramData\wg-quic\runtime\run-old\wg-quic-quick.exe`
	serviceName := windowsServiceName("office")
	service := &fakeRuntimeLifecycleService{
		manager: manager,
		name:    serviceName,
		configuration: mgr.Config{
			BinaryPathName: runtime,
		},
		statuses: []svc.Status{{State: svc.Running}},
	}
	manager.services[serviceName] = service
	var cleaned []string
	if err := stopWindowsServiceManaged(
		context.Background(),
		manager,
		"office",
		false,
		fakeRuntimeOperations("", &cleaned),
	); err != nil {
		t.Fatal(err)
	}
	if service.stopCalls != 1 || service.deleteCalls != 1 {
		t.Fatalf(
			"normal down stop/delete calls = %d/%d",
			service.stopCalls,
			service.deleteCalls,
		)
	}
	if len(cleaned) != 1 || cleaned[0] != runtime {
		t.Fatalf("normal down cleaned runtimes = %q", cleaned)
	}
}

func TestWindowsRuntimeCleanupBeforeReplacingStoppedService(t *testing.T) {
	manager := newFakeRuntimeLifecycleManager()
	const oldRuntime = `C:\ProgramData\wg-quic\runtime\run-old\wg-quic-quick.exe`
	const newRuntime = `C:\ProgramData\wg-quic\runtime\run-new\wg-quic-quick.exe`
	serviceName := windowsServiceName("office")
	oldService := &fakeRuntimeLifecycleService{
		manager: manager,
		name:    serviceName,
		configuration: mgr.Config{
			BinaryPathName: oldRuntime,
		},
		statuses: []svc.Status{{State: svc.Stopped}},
	}
	manager.services[serviceName] = oldService
	manager.created = &fakeRuntimeLifecycleService{
		statuses: []svc.Status{{State: svc.Running}},
	}
	var cleaned []string
	if err := startWindowsServiceManaged(
		context.Background(),
		manager,
		"office",
		false,
		fakeRuntimeOperations(newRuntime, &cleaned),
	); err != nil {
		t.Fatal(err)
	}
	if oldService.deleteCalls != 1 {
		t.Fatalf("stopped old service delete calls = %d", oldService.deleteCalls)
	}
	if len(cleaned) != 1 || cleaned[0] != oldRuntime {
		t.Fatalf("replaced service cleaned runtimes = %q", cleaned)
	}
	if manager.createCalls != 1 || manager.created.startCalls != 1 {
		t.Fatalf(
			"replacement create/start calls = %d/%d",
			manager.createCalls,
			manager.created.startCalls,
		)
	}
}

func TestWindowsRuntimeCleanupAfterServiceStartFailure(t *testing.T) {
	manager := newFakeRuntimeLifecycleManager()
	startFailure := errors.New("start failed")
	manager.created = &fakeRuntimeLifecycleService{
		statuses: []svc.Status{{State: svc.StartPending}},
		startErr: startFailure,
	}
	const runtime = `C:\ProgramData\wg-quic\runtime\run-failed\wg-quic-quick.exe`
	var cleaned []string
	err := startWindowsServiceManaged(
		context.Background(),
		manager,
		"office",
		false,
		fakeRuntimeOperations(runtime, &cleaned),
	)
	if !errors.Is(err, startFailure) {
		t.Fatalf("start failure error = %v", err)
	}
	if manager.created.stopCalls != 1 || manager.created.deleteCalls != 1 {
		t.Fatalf(
			"start failure rollback stop/delete calls = %d/%d",
			manager.created.stopCalls,
			manager.created.deleteCalls,
		)
	}
	if len(cleaned) != 1 || cleaned[0] != runtime {
		t.Fatalf("start failure cleaned runtimes = %q", cleaned)
	}
}

func TestWindowsRuntimeCleanupAfterRunningWaitFailure(t *testing.T) {
	manager := newFakeRuntimeLifecycleManager()
	manager.created = &fakeRuntimeLifecycleService{
		statuses: []svc.Status{{State: svc.StartPending}},
	}
	const runtime = `C:\ProgramData\wg-quic\runtime\run-timeout\wg-quic-quick.exe`
	var cleaned []string
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	operations := fakeRuntimeOperations(runtime, &cleaned)
	diagnosedBeforeCleanup := false
	operations.diagnose = func(string) (string, error) {
		diagnosedBeforeCleanup = len(cleaned) == 0
		return "core startup failed: fixture detail", nil
	}
	err := startWindowsServiceManaged(
		ctx,
		manager,
		"office",
		false,
		operations,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Running wait failure error = %v", err)
	}
	if !strings.Contains(err.Error(), "core startup failed: fixture detail") {
		t.Fatalf("Running wait failure omitted service diagnostics: %v", err)
	}
	if !diagnosedBeforeCleanup {
		t.Fatal("service diagnostics were read after runtime cleanup")
	}
	if manager.created.stopCalls != 1 || manager.created.deleteCalls != 1 {
		t.Fatalf(
			"Running wait rollback stop/delete calls = %d/%d",
			manager.created.stopCalls,
			manager.created.deleteCalls,
		)
	}
	if len(cleaned) != 1 || cleaned[0] != runtime {
		t.Fatalf("Running wait failure cleaned runtimes = %q", cleaned)
	}
}

func TestWindowsRuntimeDiagnosesServiceStoppedBeforeRunning(t *testing.T) {
	manager := newFakeRuntimeLifecycleManager()
	manager.created = &fakeRuntimeLifecycleService{
		statuses: []svc.Status{{
			State:         svc.Stopped,
			Win32ExitCode: 1,
		}},
	}
	const runtime = `C:\ProgramData\wg-quic\runtime\run-stopped\wg-quic-quick.exe`
	var cleaned []string
	operations := fakeRuntimeOperations(runtime, &cleaned)
	operations.diagnose = func(executable string) (string, error) {
		if executable != runtime {
			t.Fatalf("diagnostic executable = %q, want %q", executable, runtime)
		}
		if len(cleaned) != 0 {
			t.Fatal("service diagnostics were read after runtime cleanup")
		}
		return "prepare Wintun: fixture failure", nil
	}
	err := startWindowsServiceManaged(
		context.Background(),
		manager,
		"office",
		false,
		operations,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"stopped before reaching state Running",
	) {
		t.Fatalf("stopped service error = %v", err)
	}
	if !strings.Contains(err.Error(), "prepare Wintun: fixture failure") {
		t.Fatalf("stopped service omitted diagnostics: %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != runtime {
		t.Fatalf("stopped service cleaned runtimes = %q", cleaned)
	}
}

func TestWindowsRuntimeMissingServiceDiagnosticPreservesPrimaryFailure(
	t *testing.T,
) {
	manager := newFakeRuntimeLifecycleManager()
	manager.created = &fakeRuntimeLifecycleService{
		statuses: []svc.Status{{State: svc.Stopped, Win32ExitCode: 1}},
	}
	const runtime = `C:\ProgramData\wg-quic\runtime\run-no-record\wg-quic-quick.exe`
	var cleaned []string
	operations := fakeRuntimeOperations(runtime, &cleaned)
	operations.diagnose = func(string) (string, error) { return "", nil }
	err := startWindowsServiceManaged(
		context.Background(), manager, "office", false, operations,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"stopped before reaching state Running",
	) {
		t.Fatalf("missing diagnostic changed primary failure: %v", err)
	}
	if strings.Contains(err.Error(), "Windows service diagnostics") {
		t.Fatalf("missing diagnostic added an empty section: %v", err)
	}
}

func TestWindowsRuntimeRollbackRetainsServiceAndRuntimeWhenStopUnconfirmed(
	t *testing.T,
) {
	originalTimeout := windowsRuntimeCleanupTimeout
	windowsRuntimeCleanupTimeout = 25 * time.Millisecond
	defer func() { windowsRuntimeCleanupTimeout = originalTimeout }()

	manager := newFakeRuntimeLifecycleManager()
	serviceName := windowsServiceName("office")
	service := &fakeRuntimeLifecycleService{
		manager:    manager,
		name:       serviceName,
		statuses:   []svc.Status{{State: svc.StartPending}},
		controlErr: windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL,
	}
	manager.services[serviceName] = service
	const runtime = `C:\ProgramData\wg-quic\runtime\run-pending\wg-quic-quick.exe`
	var cleaned []string
	stage := windowsRuntimeStage{executable: runtime}
	beforeDeleteCalls := 0
	err := rollbackWindowsStartedServiceBeforeDelete(
		manager,
		service,
		serviceName,
		&stage,
		fakeRuntimeOperations(runtime, &cleaned),
		func() error {
			beforeDeleteCalls++
			return nil
		},
	)
	if err == nil {
		t.Fatal("rollback unexpectedly confirmed the pending service stopped")
	}
	if len(cleaned) != 0 {
		t.Fatalf("unconfirmed rollback cleaned runtimes = %q", cleaned)
	}
	if service.deleteCalls != 0 || service.closeCalls != 1 {
		t.Fatalf(
			"pending rollback delete/close calls = %d/%d",
			service.deleteCalls,
			service.closeCalls,
		)
	}
	if beforeDeleteCalls != 0 {
		t.Fatalf("pending rollback before-delete calls = %d", beforeDeleteCalls)
	}
	if manager.services[serviceName] != service {
		t.Fatal("pending rollback did not retain the controllable service")
	}
	if !strings.Contains(err.Error(), "remained StartPending") {
		t.Fatalf("pending rollback error = %v", err)
	}
}

func TestWindowsRuntimeRollbackRetriesStopAfterStartPendingBecomesRunning(
	t *testing.T,
) {
	manager := newFakeRuntimeLifecycleManager()
	serviceName := windowsServiceName("office")
	service := &fakeRuntimeLifecycleService{
		manager: manager,
		name:    serviceName,
		statuses: []svc.Status{
			{State: svc.StartPending},
			{State: svc.Running},
		},
		controlErrors: []error{
			windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL,
			nil,
		},
	}
	manager.services[serviceName] = service
	const runtime = `C:\ProgramData\wg-quic\runtime\run-retry\wg-quic-quick.exe`
	var cleaned []string
	stage := windowsRuntimeStage{executable: runtime}
	beforeDeleteCalls := 0
	if err := rollbackWindowsStartedServiceBeforeDelete(
		manager,
		service,
		serviceName,
		&stage,
		fakeRuntimeOperations(runtime, &cleaned),
		func() error {
			beforeDeleteCalls++
			if service.deleteCalls != 0 || len(cleaned) != 0 {
				t.Fatal("before-delete callback ran after deletion or cleanup")
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if service.stopCalls != 2 || service.deleteCalls != 1 {
		t.Fatalf(
			"transitioning rollback stop/delete calls = %d/%d",
			service.stopCalls,
			service.deleteCalls,
		)
	}
	if beforeDeleteCalls != 1 {
		t.Fatalf("transitioning rollback before-delete calls = %d", beforeDeleteCalls)
	}
	if len(cleaned) != 1 || cleaned[0] != runtime {
		t.Fatalf("transitioning rollback cleaned runtimes = %q", cleaned)
	}
}

func TestWindowsRuntimeRollbackJoinsBeforeDeleteErrorAndStillCleansStoppedService(
	t *testing.T,
) {
	manager := newFakeRuntimeLifecycleManager()
	serviceName := windowsServiceName("office")
	service := &fakeRuntimeLifecycleService{
		manager:  manager,
		name:     serviceName,
		statuses: []svc.Status{{State: svc.Stopped}},
	}
	manager.services[serviceName] = service
	const runtime = `C:\ProgramData\wg-quic\runtime\run-diagnostic\wg-quic-quick.exe`
	var cleaned []string
	stage := windowsRuntimeStage{executable: runtime}
	diagnosticErr := errors.New("read startup diagnostic")
	err := rollbackWindowsStartedServiceBeforeDelete(
		manager,
		service,
		serviceName,
		&stage,
		fakeRuntimeOperations(runtime, &cleaned),
		func() error {
			if service.deleteCalls != 0 || len(cleaned) != 0 {
				t.Fatal("before-delete callback ran after deletion or cleanup")
			}
			return diagnosticErr
		},
	)
	if !errors.Is(err, diagnosticErr) {
		t.Fatalf("rollback error = %v, want diagnostic error", err)
	}
	if service.deleteCalls != 1 {
		t.Fatalf("stopped rollback delete calls = %d", service.deleteCalls)
	}
	if len(cleaned) != 1 || cleaned[0] != runtime {
		t.Fatalf("stopped rollback cleaned runtimes = %q", cleaned)
	}
}

func TestWindowsRuntimeReferenceScanIsFailClosed(t *testing.T) {
	programData, err := windowsProgramDataPath()
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(
		programData,
		windowsWGQUICRootDirectory,
		windowsServiceRuntimeDirectory,
		"run-0123456789abcdef0123456789abcdef",
		"wg-quic-quick.exe",
	)
	manager := newFakeRuntimeLifecycleManager()
	serviceName := windowsServiceName("office")
	manager.services[serviceName] = &fakeRuntimeLifecycleService{
		manager: manager,
		name:    serviceName,
		configuration: mgr.Config{BinaryPathName: fmt.Sprintf(
			`"%s" run office --broker-safe`,
			runtime,
		)},
	}
	references, err := windowsReferencedRuntimeDirectories(manager)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(runtime)
	if _, ok := references[windowsPathKey(directory)]; !ok {
		t.Fatalf("service reference scan omitted %q", directory)
	}

	manager.openErrors[serviceName] = windows.ERROR_SERVICE_MARKED_FOR_DELETE
	if _, err := windowsReferencedRuntimeDirectories(manager); err == nil {
		t.Fatal("reference scan ignored a marked-for-delete service")
	}
	delete(manager.openErrors, serviceName)
	manager.services[serviceName].configuration.BinaryPathName =
		`C:\Users\Public\wg-quic-quick.exe run office`
	if _, err := windowsReferencedRuntimeDirectories(manager); err == nil {
		t.Fatal("reference scan accepted an unrecognized service executable")
	}
}

func TestWindowsRuntimeCleanupGuardsCurrentProcessAndWhitelist(t *testing.T) {
	directory := `C:\ProgramData\wg-quic\runtime\run-current`
	if !windowsRuntimeDirectoryIsCurrent(
		directory,
		filepath.Join(directory, "wg-quic-quick.exe"),
	) {
		t.Fatal("current process runtime was not recognized")
	}
	originalCurrentExecutable := windowsRuntimeCurrentExecutable
	defer func() {
		windowsRuntimeCurrentExecutable = originalCurrentExecutable
	}()
	windowsRuntimeCurrentExecutable = func() (string, error) {
		return "", errors.New("current executable unavailable")
	}
	if _, err := windowsRuntimeDirectoryIsCurrentProcess(directory); err == nil {
		t.Fatal("current executable failure did not retain the runtime")
	}
	for _, name := range []string{
		"wg-quic-quick.exe",
		"wg-quic.exe",
		"wintun.dll",
		windowsServiceFailureFileName,
	} {
		if !windowsRuntimeCleanupEntryAllowed(name) {
			t.Fatalf("cleanup rejected whitelisted entry %q", name)
		}
	}
	for _, name := range []string{
		"other.exe",
		"wg-quic.exe.log",
		"service-error.txt.bak",
		"SERVICE-ERROR.TXT",
		"WG-QUIC.EXE",
		"subdirectory",
	} {
		if windowsRuntimeCleanupEntryAllowed(name) {
			t.Fatalf("cleanup accepted non-whitelisted entry %q", name)
		}
	}
}

func TestWindowsRuntimeCleanupPrimitiveRejectsHardLinks(t *testing.T) {
	directory := t.TempDir()
	original := filepath.Join(directory, "wg-quic.exe")
	linked := filepath.Join(directory, "linked.exe")
	if err := os.WriteFile(original, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, linked); err != nil {
		t.Fatal(err)
	}
	file, err := openWindowsReadOnlyNoFollowFile(original)
	if file != nil {
		_ = file.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked runtime candidate error = %v", err)
	}
}

func TestWindowsRuntimeCleanupRetriesTransientImageLocks(t *testing.T) {
	attempts := 0
	err := removeWindowsTrustedRuntimeDirectoryWithRetry(
		context.Background(),
		`C:\ProgramData\wg-quic\runtime\run-retry`,
		func(string) error {
			attempts++
			if attempts < 3 {
				return windows.ERROR_SHARING_VIOLATION
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("transient runtime cleanup attempts = %d, want 3", attempts)
	}
}

func TestWindowsRuntimeSweepSecuresInterfacesBeforeRuntime(t *testing.T) {
	components := windowsSecureProgramDataLayoutComponents()
	if len(components) < 2 || len(components[0]) != 1 ||
		components[0][0].name != "interfaces" ||
		len(components[1]) != 1 ||
		components[1][0].name != windowsServiceRuntimeDirectory {
		t.Fatalf("secure ProgramData initialization order = %#v", components)
	}
}
