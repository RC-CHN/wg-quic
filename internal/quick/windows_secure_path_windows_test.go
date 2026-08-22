//go:build windows

package quick

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestWindowsDACLPolicyMapsGenericFileRights(t *testing.T) {
	generic, err := windows.SecurityDescriptorFromString(
		"O:SYD:P(A;;GRGX;;;BU)",
	)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := windows.SecurityDescriptorFromString(
		"O:SYD:P(A;;0x1200a9;;;BU)",
	)
	if err != nil {
		t.Fatal(err)
	}
	equal, err := windowsDACLPoliciesEqual(generic, mapped)
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("generic and mapped file rights were not equivalent")
	}
}

func TestWindowsDACLPolicyRejectsUnexpectedChanges(t *testing.T) {
	expected, err := windows.SecurityDescriptorFromString(
		"O:SYD:P(A;;FA;;;SY)(A;;GRGX;;;BU)",
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		sddl string
	}{
		{
			name: "extra ACE",
			sddl: "O:SYD:P(A;;FA;;;SY)(A;;GRGX;;;BU)(A;;GR;;;WD)",
		},
		{
			name: "changed flags",
			sddl: "O:SYD:P(A;;FA;;;SY)(A;CI;GRGX;;;BU)",
		},
		{
			name: "changed SID",
			sddl: "O:SYD:P(A;;FA;;;SY)(A;;GRGX;;;WD)",
		},
		{
			name: "changed mask",
			sddl: "O:SYD:P(A;;FA;;;SY)(A;;GRGWGX;;;BU)",
		},
		{
			name: "changed order",
			sddl: "O:SYD:P(A;;GRGX;;;BU)(A;;FA;;;SY)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			equal, err := windowsDACLPoliciesEqual(actual, expected)
			if err != nil {
				t.Fatal(err)
			}
			if equal {
				t.Fatalf("unexpected DACL %q matched the fixed policy", test.sddl)
			}
		})
	}
	t.Run("deny ACE", func(t *testing.T) {
		actual, err := windows.SecurityDescriptorFromString(
			"O:SYD:P(A;;FA;;;SY)(D;;GRGX;;;BU)",
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := windowsDACLPoliciesEqual(actual, expected); err == nil {
			t.Fatal("deny ACE was not rejected")
		}
	})
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
		!errors.Is(err, windows.ERROR_ACCESS_DENIED) &&
		!errors.Is(err, windows.STATUS_ACCESS_DENIED) {
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

func TestWindowsSecurityHandlesCoexistWithoutDeleteAccess(
	t *testing.T,
) {
	directory := filepath.Join(t.TempDir(), "secure")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := openWindowsDirectoryForSecurity(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(first)

	second, err := openWindowsDirectoryForSecurity(directory)
	if err != nil {
		t.Fatalf("open a second directory security lease: %v", err)
	}
	defer windows.CloseHandle(second)

	firstIsolation, err := openWindowsDirectoryForIsolation(directory)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return
	}
	if err != nil {
		t.Fatalf("open directory for first isolation lease: %v", err)
	}
	defer windows.CloseHandle(firstIsolation)

	secondIsolation, err := openWindowsDirectoryForIsolation(directory)
	if err == nil {
		windows.CloseHandle(secondIsolation)
		if windows.NewLazySystemDLL("ntdll.dll").
			NewProc("wine_get_version").Find() == nil {
			t.Log("Wine does not enforce native directory share-delete leases")
			return
		}
		t.Fatal("two DELETE-capable directory leases unexpectedly coexisted")
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("open directory for second isolation lease: %v", err)
	}
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

func TestWindowsLegacyMigrationServiceNamesCombineSCMAndProfiles(t *testing.T) {
	legacyRoot := t.TempDir()
	interfaces := filepath.Join(legacyRoot, "interfaces")
	if err := os.Mkdir(interfaces, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"stored.conf",
		"ignored.txt",
		"trailing..conf",
	} {
		if err := os.WriteFile(filepath.Join(interfaces, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(interfaces, "directory.conf"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := windowsLegacyMigrationServiceNames(legacyRoot, []string{
		"unrelated-service",
		"Wg-Quic-Quick@listed",
		"WG-QUIC-QUICK@stored",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Wg-Quic-Quick@listed",
		"WG-QUIC-QUICK@stored",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy migration service names = %#v, want %#v", got, want)
	}
}

func TestWindowsLegacyMigrationServiceNamesAllowMissingInterfaces(t *testing.T) {
	got, err := windowsLegacyMigrationServiceNames(t.TempDir(), []string{
		"wg-quic-quick@listed",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wg-quic-quick@listed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy migration service names = %#v, want %#v", got, want)
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
