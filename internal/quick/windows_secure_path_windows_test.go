//go:build windows

package quick

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDACLACEsIgnoresDescriptorControlFlags(t *testing.T) {
	want := "(A;;FA;;;SY)(A;;FA;;;BA)"
	for _, descriptor := range []string{
		"O:SYD:P" + want,
		"O:SYD:PAI" + want,
	} {
		if got := windowsDACLACEs(descriptor); got != want {
			t.Fatalf("windowsDACLACEs(%q) = %q, want %q", descriptor, got, want)
		}
	}
	if got := windowsDACLACEs("O:SY"); got != "" {
		t.Fatalf("descriptor without DACL returned %q", got)
	}
}

func TestWindowsHandleRenameWithOpenDescendantIsAtomic(t *testing.T) {
	parent := t.TempDir()
	legacy := filepath.Join(parent, "legacy")
	interfaces := filepath.Join(legacy, "interfaces")
	if err := os.MkdirAll(interfaces, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(interfaces, "office.conf")
	if err := os.WriteFile(configuration, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentHandle, err := openWindowsDirectoryNoFollow(
		parent,
		windows.FILE_READ_ATTRIBUTES,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(parentHandle)
	legacyHandle, err := openWindowsDirectoryNoFollow(
		legacy,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(legacyHandle)
	child, err := openWindowsReadOnlyNoFollowFile(configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	err = renameWindowsHandleRelative(
		legacyHandle,
		parentHandle,
		"quarantine",
	)
	oldExists := pathExistsForWindowsTest(legacy)
	newExists := pathExistsForWindowsTest(filepath.Join(parent, "quarantine"))
	if err == nil {
		if oldExists || !newExists {
			t.Fatalf(
				"successful handle rename left old=%v new=%v",
				oldExists,
				newExists,
			)
		}
		return
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) &&
		!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("handle rename with pinned descendant failed unexpectedly: %v", err)
	}
	if !oldExists || newExists {
		t.Fatalf(
			"failed handle rename was not atomic: old=%v new=%v error=%v",
			oldExists,
			newExists,
			err,
		)
	}
	t.Logf("Windows refused parent rename while descendant denied share-delete: %v", err)
}

func TestWindowsLegacyRootMigrationQuarantinesAndCopiesOnlyHookFreeConfigs(
	t *testing.T,
) {
	programData := t.TempDir()
	legacyRoot := filepath.Join(programData, windowsWGQUICRootDirectory)
	legacyInterfaces := filepath.Join(legacyRoot, "interfaces")
	if err := os.MkdirAll(legacyInterfaces, 0o777); err != nil {
		t.Fatal(err)
	}
	const base = "[Interface]\nPrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
	if err := os.WriteFile(
		filepath.Join(legacyInterfaces, "clean.conf"),
		[]byte(base),
		0o666,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(legacyInterfaces, "hooked.conf"),
		[]byte(base+"PreUp = whoami\n"),
		0o666,
	); err != nil {
		t.Fatal(err)
	}

	programDataHandle, err := openWindowsDirectoryNoFollow(
		programData,
		windows.FILE_READ_ATTRIBUTES,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease := &windowsSecurePathLease{}
	lease.append(programDataHandle)
	defer lease.Close()
	rootHandle, migration, err := ensureWindowsSecureProductRoot(
		programData,
		programDataHandle,
		lease,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease.append(rootHandle)
	if migration == nil {
		t.Fatal("legacy permissive root was trusted in place")
	}
	if !strings.HasPrefix(
		filepath.Base(migration.quarantinePath),
		".wg-quic-quarantine-",
	) {
		t.Fatalf("unexpected quarantine path %q", migration.quarantinePath)
	}
	newInterfaces := filepath.Join(legacyRoot, "interfaces")
	interfacesHandle, err := ensureWindowsSecureDirectory(
		newInterfaces,
		windowsInterfacesSDDL,
		"test migrated interfaces directory",
	)
	if err != nil {
		t.Fatal(err)
	}
	lease.append(interfacesHandle)
	if err := migrateLegacyWindowsConfigurations(
		migration,
		newInterfaces,
	); err != nil {
		t.Fatal(err)
	}
	if !pathExistsForWindowsTest(filepath.Join(newInterfaces, "clean.conf")) {
		t.Fatal("hook-free legacy configuration was not migrated")
	}
	if pathExistsForWindowsTest(filepath.Join(newInterfaces, "hooked.conf")) {
		t.Fatal("hook-bearing untrusted configuration entered the trusted store")
	}
	if !pathExistsForWindowsTest(filepath.Join(
		migration.quarantinePath,
		"interfaces",
		"hooked.conf",
	)) {
		t.Fatal("skipped hook-bearing configuration was not retained in quarantine")
	}

	strictDirectory, err := windows.SecurityDescriptorFromString(
		windowsStrictDirectorySDDL,
	)
	if err != nil {
		t.Fatal(err)
	}
	interfacesDescriptor, err := windows.SecurityDescriptorFromString(
		windowsInterfacesSDDL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsPathHandleSecurity(
		rootHandle,
		strictDirectory,
		"migrated product root",
	); err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsPathHandleSecurity(
		interfacesHandle,
		interfacesDescriptor,
		"migrated interfaces directory",
	); err != nil {
		t.Fatal(err)
	}
	configFile, err := openWindowsSecureExistingFile(
		filepath.Join(newInterfaces, "clean.conf"),
		"migrated configuration test",
	)
	if err != nil {
		t.Fatal(err)
	}
	configDescriptor, err := windows.SecurityDescriptorFromString(
		windowsStrictFileSDDL,
	)
	if err != nil {
		configFile.Close()
		t.Fatal(err)
	}
	if err := verifyWindowsPathHandleSecurity(
		windows.Handle(configFile.Fd()),
		configDescriptor,
		"migrated configuration",
	); err != nil {
		configFile.Close()
		t.Fatal(err)
	}
	if err := configFile.Close(); err != nil {
		t.Fatal(err)
	}

	for _, relative := range []string{"runtime", "state"} {
		directory := filepath.Join(legacyRoot, relative)
		handle, err := ensureWindowsSecureDirectory(
			directory,
			windowsStrictDirectorySDDL,
			"test "+relative+" directory",
		)
		if err != nil {
			t.Fatal(err)
		}
		lease.append(handle)
		if err := verifyWindowsPathHandleSecurity(
			handle,
			strictDirectory,
			relative+" directory",
		); err != nil {
			t.Fatal(err)
		}
	}
	owners := filepath.Join(legacyRoot, "state", "owners")
	ownersHandle, err := ensureWindowsSecureDirectory(
		owners,
		windowsStrictDirectorySDDL,
		"test owners directory",
	)
	if err != nil {
		t.Fatal(err)
	}
	lease.append(ownersHandle)
	if err := verifyWindowsPathHandleSecurity(
		ownersHandle,
		strictDirectory,
		"owners directory",
	); err != nil {
		t.Fatal(err)
	}
	runtimeTarget, targetHandle, err := createWindowsSecureChildDirectory(
		filepath.Join(legacyRoot, "runtime"),
		"run-",
	)
	if err != nil {
		t.Fatal(err)
	}
	lease.append(targetHandle)
	runtimeFile, err := createWindowsSecureFile(
		filepath.Join(runtimeTarget, "wg-quic.exe"),
		"test runtime file",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsPathHandleSecurity(
		windows.Handle(runtimeFile.Fd()),
		configDescriptor,
		"runtime file",
	); err != nil {
		runtimeFile.Close()
		t.Fatal(err)
	}
	if err := runtimeFile.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsLegacyMigrationLimitsAreFailClosed(t *testing.T) {
	directory := t.TempDir()
	for index := 0; index <= maxWindowsLegacyMigrationEntries; index++ {
		path := filepath.Join(directory, fmt.Sprintf("entry-%04d", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readWindowsLegacyMigrationEntries(directory); !errors.Is(
		err,
		errWindowsLegacyMigrationLimit,
	) {
		t.Fatalf("legacy entry overflow error = %v", err)
	}

	oversized := filepath.Join(t.TempDir(), "oversized.conf")
	if err := os.WriteFile(
		oversized,
		make([]byte, maxWindowsLegacyMigrationFileBytes+1),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := readWindowsSecureMigrationConfig(oversized); !errors.Is(
		err,
		errWindowsLegacyMigrationLimit,
	) {
		t.Fatalf("legacy file overflow error = %v", err)
	}

	total, err := addWindowsLegacyMigrationBytes(
		maxWindowsLegacyMigrationTotalBytes-maxWindowsLegacyMigrationFileBytes,
		maxWindowsLegacyMigrationFileBytes,
	)
	if err != nil || total != maxWindowsLegacyMigrationTotalBytes {
		t.Fatalf("legacy total boundary = %d, %v", total, err)
	}
	if _, err := addWindowsLegacyMigrationBytes(total, 1); !errors.Is(
		err,
		errWindowsLegacyMigrationLimit,
	) {
		t.Fatalf("legacy total overflow error = %v", err)
	}
}

func TestWindowsInstalledPathMutationRightsIncludeDeleteChild(t *testing.T) {
	if windowsFileMutationRights()&windowsFileDeleteChild == 0 {
		t.Fatal("installed path provenance omitted FILE_DELETE_CHILD")
	}
}

func pathExistsForWindowsTest(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
