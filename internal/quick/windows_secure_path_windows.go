//go:build windows

package quick

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	windowsWGQUICRootDirectory = "wg-quic"

	// Legacy migration runs before the persistent management service reports
	// ready. Bound both directory enumeration and retained configuration data
	// so an untrusted pre-install ProgramData tree cannot hold SCM startup open
	// indefinitely or force unbounded memory use. Limits are checked for the
	// complete batch before any configuration is copied into the trusted root.
	maxWindowsLegacyMigrationEntries    = 1024
	maxWindowsLegacyMigrationFileBytes  = maxWindowsDesktopConfigSize
	maxWindowsLegacyMigrationTotalBytes = 64 * 1024 * 1024
	// FILE_DELETE_CHILD is not currently exported by x/sys/windows.
	windowsFileDeleteChild windows.ACCESS_MASK = 0x00000040

	windowsStrictDirectorySDDL = "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	windowsInterfacesSDDL      = "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;GRGX;;;BU)"
	windowsStrictFileSDDL      = "O:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)"
)

var windowsFileSecurityPrivilegeLock sync.Mutex

var errWindowsUntrustedExistingPath = errors.New(
	"untrusted existing Windows privileged path",
)

type windowsSecurePathLease struct {
	handles []windows.Handle
}

func (l *windowsSecurePathLease) Close() error {
	if l == nil {
		return nil
	}
	var errs []error
	for index := len(l.handles) - 1; index >= 0; index-- {
		if l.handles[index] == 0 || l.handles[index] == windows.InvalidHandle {
			continue
		}
		if err := windows.CloseHandle(l.handles[index]); err != nil {
			errs = append(errs, err)
		}
	}
	l.handles = nil
	return errors.Join(errs...)
}

func (l *windowsSecurePathLease) append(handle windows.Handle) {
	l.handles = append(l.handles, handle)
}

// windowsProgramDataPath deliberately uses the machine known-folder API.
// ProgramData inherited through a desktop process environment is caller
// controlled and must not select a destination for a LocalSystem operation.
func windowsProgramDataPath() (string, error) {
	path, err := windows.KnownFolderPath(
		windows.FOLDERID_ProgramData,
		windows.KF_FLAG_DEFAULT,
	)
	if err != nil {
		return "", fmt.Errorf("resolve Windows ProgramData known folder: %w", err)
	}
	path = filepath.Clean(path)
	if path == "." || !filepath.IsAbs(path) {
		return "", fmt.Errorf(
			"Windows ProgramData known folder returned invalid path %q",
			path,
		)
	}
	return path, nil
}

// ensureWindowsProgramDataLayout creates and repairs the complete set of
// privileged directories before the persistent broker accepts requests. Every
// component is opened without following a final reparse point and without
// FILE_SHARE_DELETE, then secured through that handle.
func ensureWindowsProgramDataLayout() error {
	for _, components := range windowsSecureProgramDataLayoutComponents() {
		_, lease, err := openWindowsSecureProgramDataDirectory(components)
		if err != nil {
			return err
		}
		if err := lease.Close(); err != nil {
			return fmt.Errorf("close secured Windows ProgramData directories: %w", err)
		}
	}
	return nil
}

func windowsSecureProgramDataLayoutComponents() [][]windowsSecureDirectoryComponent {
	return [][]windowsSecureDirectoryComponent{
		{{name: "interfaces", sddl: windowsInterfacesSDDL}},
		{{name: windowsServiceRuntimeDirectory, sddl: windowsStrictDirectorySDDL}},
		{
			{name: "state", sddl: windowsStrictDirectorySDDL},
			{name: "owners", sddl: windowsStrictDirectorySDDL},
		},
	}
}

type windowsSecureDirectoryComponent struct {
	name string
	sddl string
}

func openWindowsSecureInterfacesDirectory() (
	string,
	*windowsSecurePathLease,
	error,
) {
	return openWindowsSecureProgramDataDirectory(
		[]windowsSecureDirectoryComponent{{
			name: "interfaces",
			sddl: windowsInterfacesSDDL,
		}},
	)
}

func openWindowsSecureRuntimeRoot() (
	string,
	*windowsSecurePathLease,
	error,
) {
	return openWindowsSecureProgramDataDirectory(
		[]windowsSecureDirectoryComponent{{
			name: windowsServiceRuntimeDirectory,
			sddl: windowsStrictDirectorySDDL,
		}},
	)
}

func openWindowsSecureProgramDataDirectory(
	components []windowsSecureDirectoryComponent,
) (path string, lease *windowsSecurePathLease, err error) {
	programData, err := windowsProgramDataPath()
	if err != nil {
		return "", nil, err
	}
	lease = &windowsSecurePathLease{}
	defer func() {
		if err != nil {
			_ = lease.Close()
			lease = nil
		}
	}()

	programDataHandle, err := openWindowsDirectoryNoFollow(
		programData,
		windows.FILE_READ_ATTRIBUTES,
	)
	if err != nil {
		return "", nil, fmt.Errorf("open Windows ProgramData directory: %w", err)
	}
	lease.append(programDataHandle)
	if err := inspectWindowsPathHandle(
		programDataHandle, programData, true, false,
	); err != nil {
		return "", nil, err
	}

	path = filepath.Join(programData, windowsWGQUICRootDirectory)
	rootHandle, migration, err := ensureWindowsSecureProductRoot(
		programData,
		programDataHandle,
		lease,
	)
	if err != nil {
		return "", nil, err
	}
	lease.append(rootHandle)

	for _, component := range components {
		if err := validateWindowsSecureChildName(component.name); err != nil {
			return "", nil, err
		}
		sddl := component.sddl
		if sddl == "" {
			sddl = windowsStrictDirectorySDDL
		}
		path = filepath.Join(path, component.name)
		handle, err := ensureWindowsSecureDirectory(
			path,
			sddl,
			component.name+" directory",
		)
		if err != nil {
			return "", nil, err
		}
		lease.append(handle)
		if migration != nil && component.name == "interfaces" {
			if err := migrateLegacyWindowsConfigurations(
				migration,
				path,
			); err != nil {
				return "", nil, err
			}
			migration = nil
		}
	}
	return path, lease, nil
}

type windowsLegacyRootMigration struct {
	quarantinePath       string
	legacyInterfacesPath string
}

type windowsLegacyMigrationConfig struct {
	name     string
	contents []byte
}

func ensureWindowsSecureProductRoot(
	programData string,
	programDataHandle windows.Handle,
	lease *windowsSecurePathLease,
) (windows.Handle, *windowsLegacyRootMigration, error) {
	root := filepath.Join(programData, windowsWGQUICRootDirectory)
	handle, err := ensureWindowsSecureDirectory(
		root,
		windowsStrictDirectorySDDL,
		"ProgramData root",
	)
	if err == nil {
		return handle, nil, nil
	}
	if !errors.Is(err, errWindowsUntrustedExistingPath) {
		return 0, nil, err
	}
	if err := refuseWindowsLegacyMigrationWhileTunnelActive(); err != nil {
		return 0, nil, err
	}

	var legacyRoot windows.Handle
	err = withWindowsFileSecurityPrivileges(func() error {
		var openErr error
		legacyRoot, openErr = openWindowsDirectoryForSecurity(root)
		if openErr != nil {
			return fmt.Errorf("open legacy ProgramData root for isolation: %w", openErr)
		}
		if inspectErr := inspectWindowsPathHandle(
			legacyRoot, root, true, false,
		); inspectErr != nil {
			windows.CloseHandle(legacyRoot)
			legacyRoot = 0
			return inspectErr
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}

	quarantineName, err := randomWindowsSecureChildName(
		".wg-quic-quarantine-",
	)
	if err != nil {
		windows.CloseHandle(legacyRoot)
		return 0, nil, err
	}
	if err := renameWindowsHandleRelative(
		legacyRoot,
		programDataHandle,
		quarantineName,
	); err != nil {
		windows.CloseHandle(legacyRoot)
		return 0, nil, fmt.Errorf("isolate legacy ProgramData tree: %w", err)
	}
	quarantinePath := filepath.Join(programData, quarantineName)
	if err := withWindowsFileSecurityPrivileges(func() error {
		return secureWindowsPathHandle(
			legacyRoot,
			windowsStrictDirectorySDDL,
			"quarantined legacy ProgramData root",
		)
	}); err != nil {
		windows.CloseHandle(legacyRoot)
		return 0, nil, err
	}
	legacyInterfaces := filepath.Join(quarantinePath, "interfaces")
	legacyInterfacesHandle, interfacesErr := openWindowsDirectoryNoFollow(
		legacyInterfaces,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES,
	)
	if interfacesErr == nil {
		if inspectErr := inspectWindowsPathHandle(
			legacyInterfacesHandle,
			legacyInterfaces,
			true,
			false,
		); inspectErr != nil {
			windows.CloseHandle(legacyInterfacesHandle)
			windows.CloseHandle(legacyRoot)
			return 0, nil, inspectErr
		}
	} else if !errors.Is(interfacesErr, windows.ERROR_FILE_NOT_FOUND) &&
		!errors.Is(interfacesErr, windows.ERROR_PATH_NOT_FOUND) {
		windows.CloseHandle(legacyRoot)
		return 0, nil, fmt.Errorf(
			"pin quarantined interfaces directory for migration: %w",
			interfacesErr,
		)
	}
	lease.append(legacyRoot)
	if legacyInterfacesHandle != 0 {
		lease.append(legacyInterfacesHandle)
	}

	newRoot, err := createWindowsSecureDirectoryExclusive(
		root,
		windowsStrictDirectorySDDL,
		"ProgramData root after legacy isolation",
	)
	if err != nil {
		return 0, nil, fmt.Errorf("create clean ProgramData root: %w", err)
	}
	migration := &windowsLegacyRootMigration{
		quarantinePath: quarantinePath,
	}
	if legacyInterfacesHandle != 0 {
		migration.legacyInterfacesPath = filepath.Join(
			quarantinePath,
			"interfaces",
		)
	}
	return newRoot, migration, nil
}

func refuseWindowsLegacyMigrationWhileTunnelActive() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("inspect tunnel services before ProgramData migration: %w", err)
	}
	defer manager.Disconnect()
	names, err := manager.ListServices()
	if err != nil {
		return fmt.Errorf("list tunnel services before ProgramData migration: %w", err)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, windowsServicePrefix) {
			continue
		}
		service, openErr := manager.OpenService(name)
		if openErr != nil {
			return fmt.Errorf("inspect tunnel service %s before ProgramData migration: %w", name, openErr)
		}
		status, queryErr := service.Query()
		service.Close()
		if queryErr != nil {
			return fmt.Errorf("query tunnel service %s before ProgramData migration: %w", name, queryErr)
		}
		if status.State != svc.Stopped {
			return fmt.Errorf(
				"legacy ProgramData migration requires all tunnels to be inactive; service %s is %s; deactivate it first",
				name,
				windowsServiceStateName(status.State),
			)
		}
	}
	return nil
}

type windowsFileRenameInformation struct {
	replaceIfExists uint32
	rootDirectory   windows.Handle
	fileNameLength  uint32
	fileName        [1]uint16
}

func renameWindowsHandleRelative(
	handle windows.Handle,
	parent windows.Handle,
	newName string,
) error {
	nameUTF16, err := windows.UTF16FromString(newName)
	if err != nil {
		return err
	}
	nameUTF16 = nameUTF16[:len(nameUTF16)-1]
	var layout windowsFileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.fileName)) + len(nameUTF16)*2
	buffer := make([]byte, bufferSize)
	information := (*windowsFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.rootDirectory = parent
	information.fileNameLength = uint32(len(nameUTF16) * 2)
	copy(
		(*[windows.MAX_LONG_PATH]uint16)(
			unsafe.Pointer(&information.fileName[0]),
		)[:len(nameUTF16):len(nameUTF16)],
		nameUTF16,
	)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(
		handle,
		&status,
		&buffer[0],
		uint32(len(buffer)),
		windows.FileRenameInformation,
	)
}

func migrateLegacyWindowsConfigurations(
	migration *windowsLegacyRootMigration,
	destinationDirectory string,
) error {
	if migration == nil || migration.legacyInterfacesPath == "" {
		return nil
	}
	entries, err := readWindowsLegacyMigrationEntries(
		migration.legacyInterfacesPath,
	)
	if err != nil {
		return fmt.Errorf(
			"read quarantined legacy configurations at %q: %w",
			migration.quarantinePath,
			err,
		)
	}
	candidates := make([]windowsLegacyMigrationConfig, 0, len(entries))
	totalBytes := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".conf") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if platform.Current().ValidateInterfaceName(name) != nil {
			continue
		}
		sourcePath := filepath.Join(migration.legacyInterfacesPath, entry.Name())
		contents, readErr := readWindowsSecureMigrationConfig(sourcePath)
		if readErr != nil {
			if errors.Is(readErr, errWindowsLegacyMigrationLimit) {
				return fmt.Errorf(
					"legacy configuration %q in quarantine %q exceeds migration limits: %w",
					entry.Name(),
					migration.quarantinePath,
					readErr,
				)
			}
			continue
		}
		totalBytes, err = addWindowsLegacyMigrationBytes(
			totalBytes,
			len(contents),
		)
		if err != nil {
			return fmt.Errorf(
				"legacy configurations in quarantine %q exceed migration limits: %w",
				migration.quarantinePath,
				err,
			)
		}
		cfg, parseErr := config.Parse(bytes.NewReader(contents))
		if parseErr != nil || validateConfig(cfg) != nil ||
			validateWindowsManagementHookFreeConfig(cfg) != nil {
			continue
		}
		candidates = append(candidates, windowsLegacyMigrationConfig{
			name:     name,
			contents: contents,
		})
	}
	for _, candidate := range candidates {
		destination := filepath.Join(
			destinationDirectory,
			candidate.name+".conf",
		)
		if err := writeWindowsSecureMigrationConfig(
			destination,
			candidate.contents,
		); err != nil {
			return fmt.Errorf(
				"migrate legacy configuration %q from quarantine %q: %w",
				candidate.name,
				migration.quarantinePath,
				err,
			)
		}
	}
	return nil
}

var errWindowsLegacyMigrationLimit = errors.New(
	"Windows legacy configuration migration limit exceeded",
)

func readWindowsLegacyMigrationEntries(path string) ([]os.DirEntry, error) {
	handle, err := openWindowsDirectoryNoFollow(
		path,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES,
	)
	if err != nil {
		return nil, err
	}
	if err := inspectWindowsPathHandle(handle, path, true, false); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	directory := os.NewFile(uintptr(handle), path)
	entries, readErr := directory.ReadDir(
		maxWindowsLegacyMigrationEntries + 1,
	)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > maxWindowsLegacyMigrationEntries {
		return nil, fmt.Errorf(
			"%w: legacy interfaces directory has more than %d entries",
			errWindowsLegacyMigrationLimit,
			maxWindowsLegacyMigrationEntries,
		)
	}
	return entries, nil
}

func addWindowsLegacyMigrationBytes(total int, next int) (int, error) {
	if next < 0 || next > maxWindowsLegacyMigrationFileBytes {
		return total, fmt.Errorf(
			"%w: one configuration has %d bytes; maximum is %d",
			errWindowsLegacyMigrationLimit,
			next,
			maxWindowsLegacyMigrationFileBytes,
		)
	}
	if total < 0 || total > maxWindowsLegacyMigrationTotalBytes-next {
		return total, fmt.Errorf(
			"%w: configuration data exceeds %d bytes",
			errWindowsLegacyMigrationLimit,
			maxWindowsLegacyMigrationTotalBytes,
		)
	}
	return total + next, nil
}

func readWindowsSecureMigrationConfig(path string) ([]byte, error) {
	file, err := openWindowsReadOnlyNoFollowFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(
		file,
		maxWindowsLegacyMigrationFileBytes+1,
	))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxWindowsLegacyMigrationFileBytes {
		return nil, fmt.Errorf(
			"%w: configuration has more than %d bytes",
			errWindowsLegacyMigrationLimit,
			maxWindowsLegacyMigrationFileBytes,
		)
	}
	if len(contents) == 0 {
		return nil, errors.New("legacy configuration has invalid size")
	}
	return contents, nil
}

func writeWindowsSecureMigrationConfig(
	path string,
	contents []byte,
) (returnErr error) {
	file, err := createWindowsSecureFile(path, "migrated tunnel configuration")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		closeErr := file.Close()
		if !keep {
			_ = os.Remove(path)
		}
		if returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	keep = true
	return nil
}

func validateWindowsSecureChildName(name string) error {
	if name == "" || name == "." || name == ".." ||
		filepath.Base(name) != name ||
		strings.ContainsAny(name, `\/:`) {
		return fmt.Errorf("invalid secure Windows child name %q", name)
	}
	return nil
}

func ensureWindowsSecureDirectory(
	path string,
	sddl string,
	description string,
) (windows.Handle, error) {
	var handle windows.Handle
	err := withWindowsFileSecurityPrivileges(func() error {
		descriptor, descriptorErr := windows.SecurityDescriptorFromString(sddl)
		if descriptorErr != nil {
			return fmt.Errorf(
				"create Windows %s security descriptor: %w",
				description,
				descriptorErr,
			)
		}
		pathUTF16, pathErr := windows.UTF16PtrFromString(path)
		if pathErr != nil {
			return pathErr
		}
		attributes := &windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: descriptor,
		}
		createErr := windows.CreateDirectory(pathUTF16, attributes)
		runtime.KeepAlive(descriptor)
		if createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
			return fmt.Errorf("create Windows %s %q: %w", description, path, createErr)
		}
		created := createErr == nil

		opened, openErr := openWindowsDirectoryForSecurity(path)
		if openErr != nil {
			return fmt.Errorf("open Windows %s %q: %w", description, path, openErr)
		}
		if inspectErr := inspectWindowsPathHandle(
			opened, path, true, false,
		); inspectErr != nil {
			windows.CloseHandle(opened)
			return inspectErr
		}
		if !created {
			if provenanceErr := verifyWindowsExistingPathProvenance(
				opened, descriptor, description,
			); provenanceErr != nil {
				windows.CloseHandle(opened)
				return provenanceErr
			}
		}
		if secureErr := secureWindowsPathHandle(
			opened, sddl, description,
		); secureErr != nil {
			windows.CloseHandle(opened)
			return secureErr
		}
		handle = opened
		return nil
	})
	return handle, err
}

func openWindowsDirectoryNoFollow(
	path string,
	desiredAccess uint32,
) (windows.Handle, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		pathUTF16,
		desiredAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func openWindowsDirectoryForSecurity(path string) (windows.Handle, error) {
	const desiredAccess = windows.FILE_READ_ATTRIBUTES |
		windows.READ_CONTROL |
		windows.WRITE_DAC |
		windows.WRITE_OWNER |
		windows.DELETE
	return openWindowsDirectoryNoFollow(path, desiredAccess)
}

func inspectWindowsPathHandle(
	handle windows.Handle,
	path string,
	wantDirectory bool,
	requireSingleLink bool,
) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return fmt.Errorf("inspect secure Windows path %q: %w", path, err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("secure Windows path %q is a reparse point", path)
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != wantDirectory {
		kind := "file"
		if wantDirectory {
			kind = "directory"
		}
		return fmt.Errorf("secure Windows path %q is not a %s", path, kind)
	}
	if requireSingleLink && information.NumberOfLinks != 1 {
		return fmt.Errorf(
			"secure Windows file %q has %d hard links; want exactly one",
			path,
			information.NumberOfLinks,
		)
	}
	return nil
}

func secureWindowsPathHandle(
	handle windows.Handle,
	sddl string,
	description string,
) error {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("create Windows %s ACL: %w", description, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("read Windows %s owner: %w", description, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("read Windows %s DACL: %w", description, err)
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|
			windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("protect Windows %s through its handle: %w", description, err)
	}
	return verifyWindowsPathHandleSecurity(handle, descriptor, description)
}

func verifyWindowsPathHandleSecurity(
	handle windows.Handle,
	expected *windows.SECURITY_DESCRIPTOR,
	description string,
) error {
	actual, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|
			windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("verify Windows %s security: %w", description, err)
	}
	actualOwner, _, err := actual.Owner()
	if err != nil || actualOwner == nil {
		return fmt.Errorf("verify Windows %s owner: %w", description, err)
	}
	expectedOwner, _, err := expected.Owner()
	if err != nil || expectedOwner == nil {
		return fmt.Errorf("read expected Windows %s owner: %w", description, err)
	}
	if !actualOwner.Equals(expectedOwner) {
		return fmt.Errorf("Windows %s owner is not LocalSystem", description)
	}
	control, _, err := actual.Control()
	if err != nil {
		return fmt.Errorf("verify Windows %s DACL control: %w", description, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("Windows %s DACL is not protected", description)
	}
	policiesEqual, err := windowsDACLPoliciesEqual(actual, expected)
	if err != nil {
		return fmt.Errorf("verify Windows %s DACL policy: %w", description, err)
	}
	if !policiesEqual {
		return fmt.Errorf(
			"Windows %s DACL does not match the required policy: actual=%s expected=%s",
			description,
			actual.String(),
			expected.String(),
		)
	}
	return nil
}

// verifyWindowsExistingPathProvenance runs before any ownership or DACL
// mutation. Repairing an arbitrary attacker-owned directory is unsafe because
// a handle opened before repair retains its granted child-write rights. An
// existing object is migratable only when it already has the exact protected
// policy and was owned by LocalSystem or BUILTIN\Administrators. Older
// elevated releases normally used the latter default owner.
func verifyWindowsExistingPathProvenance(
	handle windows.Handle,
	expected *windows.SECURITY_DESCRIPTOR,
	description string,
) error {
	actual, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|
			windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect existing Windows %s security: %w", description, err)
	}
	owner, _, err := actual.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("inspect existing Windows %s owner: %w", description, err)
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("create LocalSystem SID: %w", err)
	}
	administratorsSID, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return fmt.Errorf("create Administrators SID: %w", err)
	}
	if !owner.Equals(systemSID) && !owner.Equals(administratorsSID) {
		return fmt.Errorf(
			"%w: existing Windows %s has untrusted owner %s; refusing in-place repair",
			errWindowsUntrustedExistingPath,
			description,
			owner.String(),
		)
	}
	control, _, err := actual.Control()
	if err != nil {
		return fmt.Errorf("inspect existing Windows %s DACL control: %w", description, err)
	}
	policiesEqual, policyErr := windowsDACLPoliciesEqual(actual, expected)
	if policyErr != nil {
		return fmt.Errorf(
			"%w: inspect existing Windows %s DACL policy: %v",
			errWindowsUntrustedExistingPath,
			description,
			policyErr,
		)
	}
	if control&windows.SE_DACL_PROTECTED == 0 || !policiesEqual {
		return fmt.Errorf(
			"%w: existing Windows %s does not have the trusted protected DACL; refusing in-place repair: actual=%s expected=%s",
			errWindowsUntrustedExistingPath,
			description,
			actual.String(),
			expected.String(),
		)
	}
	return nil
}

// windowsDACLPoliciesEqual compares the effective file access policy rather
// than its SDDL rendering. Windows maps GENERIC_* rights to file-specific
// rights when an ACL is applied to a filesystem object, so a descriptor read
// back from NTFS can be semantically identical while rendering differently.
// Only the simple allow ACEs used by wg-quic's fixed policies are accepted;
// unfamiliar ACE shapes fail closed.
func windowsDACLPoliciesEqual(
	actual *windows.SECURITY_DESCRIPTOR,
	expected *windows.SECURITY_DESCRIPTOR,
) (bool, error) {
	actualPolicy, err := windowsNormalizedDACLPolicy(actual)
	if err != nil {
		return false, fmt.Errorf("normalize actual DACL: %w", err)
	}
	expectedPolicy, err := windowsNormalizedDACLPolicy(expected)
	if err != nil {
		return false, fmt.Errorf("normalize expected DACL: %w", err)
	}
	if len(actualPolicy) != len(expectedPolicy) {
		return false, nil
	}
	for index := range actualPolicy {
		if actualPolicy[index] != expectedPolicy[index] {
			return false, nil
		}
	}
	return true, nil
}

func windowsNormalizedDACLPolicy(
	descriptor *windows.SECURITY_DESCRIPTOR,
) ([]string, error) {
	if descriptor == nil {
		return nil, errors.New("security descriptor is nil")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return nil, err
	}
	if dacl == nil {
		return nil, errors.New("DACL is nil")
	}
	header := (*windowsACLHeader)(unsafe.Pointer(dacl))
	policy := make([]string, 0, header.aceCount)
	for index := uint32(0); index < uint32(header.aceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return nil, fmt.Errorf("read ACE %d: %w", index, err)
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			aceType := uint8(0xff)
			if ace != nil {
				aceType = ace.Header.AceType
			}
			return nil, fmt.Errorf("ACE %d has unsupported type %d", index, aceType)
		}
		sidOffset := int(unsafe.Offsetof(ace.SidStart))
		aceSize := int(ace.Header.AceSize)
		const minimumSIDSize = 8
		if aceSize < sidOffset+minimumSIDSize {
			return nil, fmt.Errorf("ACE %d has invalid size %d", index, aceSize)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sidOffset+sid.Len() != aceSize {
			return nil, fmt.Errorf("ACE %d has an invalid SID payload", index)
		}
		sidString := sid.String()
		if sidString == "" {
			return nil, fmt.Errorf("ACE %d SID cannot be rendered", index)
		}
		policy = append(policy, fmt.Sprintf(
			"%02x:%08x:%s",
			ace.Header.AceFlags,
			uint32(windowsNormalizeFileAccessMask(ace.Mask)),
			sidString,
		))
	}
	return policy, nil
}

func windowsNormalizeFileAccessMask(mask windows.ACCESS_MASK) windows.ACCESS_MASK {
	if mask&windows.GENERIC_ALL != 0 {
		mask &^= windows.GENERIC_ALL
		mask |= windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	}
	if mask&windows.GENERIC_READ != 0 {
		mask &^= windows.GENERIC_READ
		mask |= windows.FILE_GENERIC_READ
	}
	if mask&windows.GENERIC_WRITE != 0 {
		mask &^= windows.GENERIC_WRITE
		mask |= windows.FILE_GENERIC_WRITE
	}
	if mask&windows.GENERIC_EXECUTE != 0 {
		mask &^= windows.GENERIC_EXECUTE
		mask |= windows.FILE_GENERIC_EXECUTE
	}
	return mask
}

// windowsDACLACEs strips descriptor control flags such as AI. SetSecurityInfo
// can retain that informational bit on an existing protected object; the ACE
// sequence and SE_DACL_PROTECTED are the security-relevant invariants here.
func windowsDACLACEs(sddl string) string {
	daclStart := strings.Index(sddl, "D:")
	if daclStart < 0 {
		return ""
	}
	aceOffset := strings.Index(sddl[daclStart:], "(")
	if aceOffset < 0 {
		return ""
	}
	return sddl[daclStart+aceOffset:]
}

func withWindowsFileSecurityPrivileges(operation func() error) error {
	windowsFileSecurityPrivilegeLock.Lock()
	defer windowsFileSecurityPrivilegeLock.Unlock()

	var token windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES,
		&token,
	); err != nil {
		return fmt.Errorf("open process token for Windows path security: %w", err)
	}
	defer token.Close()

	type adjustedPrivilege struct {
		previous windows.Tokenprivileges
	}
	adjusted := make([]adjustedPrivilege, 0, 3)
	defer func() {
		for index := len(adjusted) - 1; index >= 0; index-- {
			_ = windows.AdjustTokenPrivileges(
				token,
				false,
				&adjusted[index].previous,
				0,
				nil,
				nil,
			)
		}
	}()

	for _, name := range []string{
		"SeBackupPrivilege",
		"SeRestorePrivilege",
		"SeTakeOwnershipPrivilege",
	} {
		nameUTF16, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return err
		}
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, nameUTF16, &luid); err != nil {
			return fmt.Errorf("look up %s: %w", name, err)
		}
		requested := windows.Tokenprivileges{PrivilegeCount: 1}
		requested.Privileges[0] = windows.LUIDAndAttributes{
			Luid:       luid,
			Attributes: windows.SE_PRIVILEGE_ENABLED,
		}
		previous := windows.Tokenprivileges{}
		var previousSize uint32
		if err := windows.AdjustTokenPrivileges(
			token,
			false,
			&requested,
			uint32(unsafe.Sizeof(previous)),
			&previous,
			&previousSize,
		); err != nil {
			return fmt.Errorf("enable %s: %w", name, err)
		}
		adjusted = append(adjusted, adjustedPrivilege{previous: previous})
	}
	return operation()
}

func randomWindowsSecureChildName(prefix string) (string, error) {
	if err := validateWindowsSecureChildName(prefix); err != nil {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate secure Windows path name: %w", err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func createWindowsSecureChildDirectory(
	parentPath string,
	prefix string,
) (path string, handle windows.Handle, err error) {
	for attempts := 0; attempts < 32; attempts++ {
		name, nameErr := randomWindowsSecureChildName(prefix)
		if nameErr != nil {
			return "", 0, nameErr
		}
		path = filepath.Join(parentPath, name)
		handle, err = createWindowsSecureDirectoryExclusive(
			path,
			windowsStrictDirectorySDDL,
			"random runtime directory",
		)
		if err == nil {
			return path, handle, nil
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return "", 0, err
		}
	}
	return "", 0, errors.New("could not allocate a unique secure Windows directory")
}

func createWindowsSecureDirectoryExclusive(
	path string,
	sddl string,
	description string,
) (windows.Handle, error) {
	var handle windows.Handle
	err := withWindowsFileSecurityPrivileges(func() error {
		descriptor, descriptorErr := windows.SecurityDescriptorFromString(sddl)
		if descriptorErr != nil {
			return fmt.Errorf(
				"create Windows %s security descriptor: %w",
				description,
				descriptorErr,
			)
		}
		pathUTF16, pathErr := windows.UTF16PtrFromString(path)
		if pathErr != nil {
			return pathErr
		}
		attributes := &windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: descriptor,
		}
		if createErr := windows.CreateDirectory(pathUTF16, attributes); createErr != nil {
			runtime.KeepAlive(descriptor)
			return createErr
		}
		runtime.KeepAlive(descriptor)
		opened, openErr := openWindowsDirectoryForSecurity(path)
		if openErr != nil {
			return fmt.Errorf("open new Windows %s %q: %w", description, path, openErr)
		}
		if inspectErr := inspectWindowsPathHandle(
			opened, path, true, false,
		); inspectErr != nil {
			windows.CloseHandle(opened)
			return inspectErr
		}
		if secureErr := secureWindowsPathHandle(
			opened, sddl, description,
		); secureErr != nil {
			windows.CloseHandle(opened)
			return secureErr
		}
		handle = opened
		return nil
	})
	return handle, err
}

func openWindowsFileNoFollow(
	path string,
	desiredAccess uint32,
	shareMode uint32,
) (windows.Handle, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		pathUTF16,
		desiredAccess,
		shareMode,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|
			windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func openWindowsReadOnlyNoFollowFile(path string) (*os.File, error) {
	handle, err := openWindowsFileNoFollow(
		path,
		windows.GENERIC_READ|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ,
	)
	if err != nil {
		return nil, err
	}
	if err := inspectWindowsPathHandle(handle, path, false, true); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func validateWindowsTrustedRuntimeExecutable(
	path string,
	wantFileName string,
) error {
	directory, err := windowsRuntimeDirectoryFromExecutablePath(
		path,
		wantFileName,
	)
	if err != nil {
		return err
	}
	fileDescriptor, err := windows.SecurityDescriptorFromString(windowsStrictFileSDDL)
	if err != nil {
		return err
	}
	lease, err := openWindowsTrustedRuntimeDirectory(directory)
	if err != nil {
		return err
	}
	defer lease.Close()

	handle, err := openWindowsFileNoFollow(
		path,
		windows.GENERIC_READ|
			windows.FILE_READ_ATTRIBUTES|
			windows.READ_CONTROL,
		windows.FILE_SHARE_READ,
	)
	if err != nil {
		return fmt.Errorf("open trusted runtime executable: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := inspectWindowsPathHandle(handle, path, false, true); err != nil {
		return err
	}
	return verifyWindowsPathHandleSecurity(
		handle,
		fileDescriptor,
		"trusted runtime executable",
	)
}

func windowsRuntimeDirectoryFromExecutablePath(
	path string,
	wantFileName string,
) (string, error) {
	path = filepath.Clean(path)
	if !strings.EqualFold(filepath.Base(path), wantFileName) {
		return "", fmt.Errorf(
			"runtime executable %q does not have filename %q",
			path,
			wantFileName,
		)
	}
	directory := filepath.Dir(path)
	if _, _, _, err := windowsTrustedRuntimeDirectoryLayout(
		directory,
	); err != nil {
		return "", err
	}
	return directory, nil
}

func windowsTrustedRuntimeDirectoryLayout(
	path string,
) (programData string, runtimeRoot string, runName string, err error) {
	programData, err = windowsProgramDataPath()
	if err != nil {
		return "", "", "", err
	}
	runtimeRoot = filepath.Join(
		programData,
		windowsWGQUICRootDirectory,
		windowsServiceRuntimeDirectory,
	)
	path = filepath.Clean(path)
	relative, relativeErr := filepath.Rel(runtimeRoot, path)
	if relativeErr != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		strings.Contains(relative, string(filepath.Separator)) {
		return "", "", "", fmt.Errorf(
			"runtime directory %q is outside the secure runtime root",
			path,
		)
	}
	if !strings.HasPrefix(relative, "run-") {
		return "", "", "", fmt.Errorf(
			"runtime directory %q does not use the secure layout",
			path,
		)
	}
	nonce := strings.TrimPrefix(relative, "run-")
	decoded, decodeErr := hex.DecodeString(nonce)
	if decodeErr != nil || len(decoded) != 16 {
		return "", "", "", fmt.Errorf(
			"runtime directory %q has an invalid nonce",
			path,
		)
	}
	return programData, runtimeRoot, relative, nil
}

func validateWindowsTrustedRuntimeDirectory(path string) error {
	lease, err := openWindowsTrustedRuntimeDirectory(path)
	if err != nil {
		return err
	}
	return lease.Close()
}

func openWindowsTrustedRuntimeDirectory(
	path string,
) (*windowsSecurePathLease, error) {
	programData, runtimeRoot, runName, err := windowsTrustedRuntimeDirectoryLayout(
		path,
	)
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		windowsStrictDirectorySDDL,
	)
	if err != nil {
		return nil, err
	}
	lease := &windowsSecurePathLease{}
	for index, directory := range []string{
		programData,
		filepath.Join(programData, windowsWGQUICRootDirectory),
		runtimeRoot,
		filepath.Join(runtimeRoot, runName),
	} {
		handle, openErr := openWindowsDirectoryNoFollow(
			directory,
			windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		)
		if openErr != nil {
			_ = lease.Close()
			return nil, fmt.Errorf(
				"open trusted runtime directory %q: %w",
				directory,
				openErr,
			)
		}
		lease.append(handle)
		if inspectErr := inspectWindowsPathHandle(
			handle,
			directory,
			true,
			false,
		); inspectErr != nil {
			_ = lease.Close()
			return nil, inspectErr
		}
		if index != 0 {
			if securityErr := verifyWindowsPathHandleSecurity(
				handle,
				descriptor,
				"trusted runtime directory",
			); securityErr != nil {
				_ = lease.Close()
				return nil, securityErr
			}
		}
	}
	return lease, nil
}

func validateWindowsTrustedInstalledFile(path string) error {
	programFiles, err := windows.KnownFolderPath(
		windows.FOLDERID_ProgramFiles,
		windows.KF_FLAG_DEFAULT,
	)
	if err != nil {
		return fmt.Errorf("resolve Windows Program Files known folder: %w", err)
	}
	programFiles = filepath.Clean(programFiles)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(programFiles, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("installed executable %q is outside Program Files", path)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) < 2 {
		return fmt.Errorf("installed executable %q has no protected application directory", path)
	}

	lease := &windowsSecurePathLease{}
	defer lease.Close()
	directories := []string{programFiles}
	directory := programFiles
	for _, part := range parts[:len(parts)-1] {
		directory = filepath.Join(directory, part)
		directories = append(directories, directory)
	}
	for _, directory := range directories {
		handle, openErr := openWindowsDirectoryNoFollow(
			directory,
			windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		)
		if openErr != nil {
			return fmt.Errorf("open installed executable directory %q: %w", directory, openErr)
		}
		lease.append(handle)
		if inspectErr := inspectWindowsPathHandle(
			handle, directory, true, false,
		); inspectErr != nil {
			return inspectErr
		}
		if securityErr := verifyWindowsInstalledPathSecurity(
			handle,
			"installed executable directory",
		); securityErr != nil {
			return securityErr
		}
	}

	handle, err := openWindowsFileNoFollow(
		path,
		windows.GENERIC_READ|
			windows.FILE_READ_ATTRIBUTES|
			windows.READ_CONTROL,
		windows.FILE_SHARE_READ,
	)
	if err != nil {
		return fmt.Errorf("open installed executable %q: %w", path, err)
	}
	defer windows.CloseHandle(handle)
	if err := inspectWindowsPathHandle(handle, path, false, true); err != nil {
		return err
	}
	return verifyWindowsInstalledPathSecurity(handle, "installed executable")
}

type windowsACLHeader struct {
	revision byte
	padding1 byte
	size     uint16
	aceCount uint16
	padding2 uint16
}

func verifyWindowsInstalledPathSecurity(
	handle windows.Handle,
	description string,
) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|
			windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect Windows %s ACL: %w", description, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("inspect Windows %s owner: %w", description, err)
	}
	if !windowsInstallerTrustedSID(owner) {
		return fmt.Errorf("Windows %s has untrusted owner %s", description, owner.String())
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("inspect Windows %s DACL: %w", description, err)
	}
	header := (*windowsACLHeader)(unsafe.Pointer(dacl))
	for index := uint32(0); index < uint32(header.aceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("inspect Windows %s ACE %d: %w", description, index, err)
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 ||
			!windowsAllowedACEType(ace.Header.AceType) ||
			ace.Mask&windowsFileMutationRights() == 0 {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("Windows %s has an unsupported write-capable object ACE", description)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windowsInstallerTrustedSID(sid) {
			return fmt.Errorf(
				"Windows %s grants mutation rights to untrusted SID %s",
				description,
				sid.String(),
			)
		}
	}
	return nil
}

func windowsAllowedACEType(aceType byte) bool {
	// ACCESS_ALLOWED_ACE, ACCESS_ALLOWED_OBJECT_ACE,
	// ACCESS_ALLOWED_CALLBACK_ACE, and ACCESS_ALLOWED_CALLBACK_OBJECT_ACE.
	switch aceType {
	case 0, 5, 9, 11:
		return true
	default:
		return false
	}
}

func windowsFileMutationRights() windows.ACCESS_MASK {
	return windows.FILE_WRITE_DATA |
		windows.FILE_APPEND_DATA |
		windowsFileDeleteChild |
		windows.FILE_WRITE_EA |
		windows.FILE_WRITE_ATTRIBUTES |
		windows.DELETE |
		windows.WRITE_DAC |
		windows.WRITE_OWNER |
		windows.GENERIC_WRITE |
		windows.GENERIC_ALL
}

func windowsInstallerTrustedSID(sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	if sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return true
	}
	trustedInstaller, _, _, err := windows.LookupSID(
		"",
		`NT SERVICE\TrustedInstaller`,
	)
	return err == nil && sid.Equals(trustedInstaller)
}

func openWindowsSecureExistingFile(
	path string,
	description string,
) (*os.File, error) {
	var file *os.File
	err := withWindowsFileSecurityPrivileges(func() error {
		descriptor, descriptorErr := windows.SecurityDescriptorFromString(
			windowsStrictFileSDDL,
		)
		if descriptorErr != nil {
			return descriptorErr
		}
		handle, openErr := openWindowsFileNoFollow(
			path,
			windows.GENERIC_READ|
				windows.FILE_READ_ATTRIBUTES|
				windows.READ_CONTROL|
				windows.WRITE_DAC|
				windows.WRITE_OWNER,
			windows.FILE_SHARE_READ,
		)
		if openErr != nil {
			return openErr
		}
		if inspectErr := inspectWindowsPathHandle(
			handle, path, false, true,
		); inspectErr != nil {
			windows.CloseHandle(handle)
			return inspectErr
		}
		if provenanceErr := verifyWindowsExistingPathProvenance(
			handle, descriptor, description,
		); provenanceErr != nil {
			windows.CloseHandle(handle)
			return provenanceErr
		}
		if secureErr := secureWindowsPathHandle(
			handle, windowsStrictFileSDDL, description,
		); secureErr != nil {
			windows.CloseHandle(handle)
			return secureErr
		}
		file = os.NewFile(uintptr(handle), path)
		return nil
	})
	return file, err
}

func createWindowsSecureFile(
	path string,
	description string,
) (*os.File, error) {
	var file *os.File
	err := withWindowsFileSecurityPrivileges(func() error {
		descriptor, descriptorErr := windows.SecurityDescriptorFromString(
			windowsStrictFileSDDL,
		)
		if descriptorErr != nil {
			return descriptorErr
		}
		pathUTF16, pathErr := windows.UTF16PtrFromString(path)
		if pathErr != nil {
			return pathErr
		}
		attributes := &windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: descriptor,
		}
		handle, createErr := windows.CreateFile(
			pathUTF16,
			windows.GENERIC_READ|
				windows.GENERIC_WRITE|
				windows.FILE_READ_ATTRIBUTES|
				windows.READ_CONTROL|
				windows.WRITE_DAC|
				windows.WRITE_OWNER,
			windows.FILE_SHARE_READ,
			attributes,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|
				windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		runtime.KeepAlive(descriptor)
		if createErr != nil {
			return createErr
		}
		if inspectErr := inspectWindowsPathHandle(
			handle, path, false, true,
		); inspectErr != nil {
			windows.CloseHandle(handle)
			return inspectErr
		}
		if secureErr := secureWindowsPathHandle(
			handle, windowsStrictFileSDDL, description,
		); secureErr != nil {
			windows.CloseHandle(handle)
			return secureErr
		}
		file = os.NewFile(uintptr(handle), path)
		return nil
	})
	return file, err
}

func createWindowsSecureTemporaryFile(
	directory string,
	prefix string,
	description string,
) (*os.File, error) {
	for attempts := 0; attempts < 32; attempts++ {
		name, err := randomWindowsSecureChildName(prefix)
		if err != nil {
			return nil, err
		}
		file, err := createWindowsSecureFile(
			filepath.Join(directory, name),
			description,
		)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, windows.ERROR_FILE_EXISTS) &&
			!errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, err
		}
	}
	return nil, errors.New("could not allocate a unique secure Windows file")
}
