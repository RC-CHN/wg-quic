//go:build windows

package quick

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsCorePortableQuickAllowsOnlyNoFollowSibling(t *testing.T) {
	directory := t.TempDir()
	current := filepath.Join(directory, "wg-quic-quick.exe")
	core := filepath.Join(directory, coreExecutableName())
	if err := os.WriteFile(current, []byte("quick"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core, []byte("core"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := platformValidateCoreExecutable(current, core); err != nil {
		t.Fatalf("portable sibling core was rejected: %v", err)
	}
	otherDirectory := filepath.Join(directory, "other")
	if err := os.Mkdir(otherDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(otherDirectory, coreExecutableName())
	if err := os.WriteFile(other, []byte("other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := platformValidateCoreExecutable(current, other); err == nil {
		t.Fatal("portable quick accepted a non-sibling core")
	}
}

func TestWindowsCoreAcceptsTrustedCustomManagerBundle(t *testing.T) {
	root, bin, manager, core := createWindowsTrustedManagerBundle(t)
	if err := platformValidateCoreExecutable(manager, core); err != nil {
		t.Fatalf("trusted custom manager bundle was rejected: %v", err)
	}
	wrongCore := filepath.Join(root, coreExecutableName())
	if err := platformValidateCoreExecutable(manager, wrongCore); err == nil {
		t.Fatal("custom manager accepted a core outside its fixed bin directory")
	}
	portableQuick := filepath.Join(root, "wg-quic-quick.exe")
	createWindowsTrustedTestFile(t, portableQuick)
	if err := platformValidateCoreExecutable(portableQuick, core); err == nil {
		t.Fatal("portable quick used the custom installed manager exception")
	}
	if filepath.Dir(core) != bin {
		t.Fatalf("custom test core directory = %q, want %q", filepath.Dir(core), bin)
	}
}

func TestWindowsCoreRejectsWritableCustomManagerBundle(t *testing.T) {
	_, _, manager, core := createWindowsTrustedManagerBundle(t)
	handle, err := openWindowsDirectoryForSecurity(filepath.Dir(core))
	if err != nil {
		t.Fatal(err)
	}
	const writableByUsers = "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)"
	securityErr := withWindowsFileSecurityPrivileges(func() error {
		return secureWindowsPathHandle(
			handle,
			writableByUsers,
			"test user-writable custom bin directory",
		)
	})
	closeErr := windows.CloseHandle(handle)
	if securityErr != nil || closeErr != nil {
		t.Fatalf("make custom bin directory unsafe: %v", errors.Join(securityErr, closeErr))
	}
	if err := platformValidateCoreExecutable(manager, core); err == nil {
		t.Fatal("custom manager accepted a user-writable bin directory")
	}
}

func createWindowsTrustedManagerBundle(
	t *testing.T,
) (root string, bin string, manager string, core string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "wg-quic-custom")
	rootHandle, err := createWindowsSecureDirectoryExclusive(
		root,
		windowsStrictDirectorySDDL,
		"test custom installation root",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CloseHandle(rootHandle); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(root, "bin")
	binHandle, err := createWindowsSecureDirectoryExclusive(
		bin,
		windowsStrictDirectorySDDL,
		"test custom installation bin directory",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CloseHandle(binHandle); err != nil {
		t.Fatal(err)
	}
	manager = filepath.Join(root, windowsManagementServiceName+".exe")
	core = filepath.Join(bin, coreExecutableName())
	createWindowsTrustedTestFile(t, manager)
	createWindowsTrustedTestFile(t, core)
	return root, bin, manager, core
}

func createWindowsTrustedTestFile(t *testing.T, path string) {
	t.Helper()
	file, err := createWindowsSecureFile(path, "test installed executable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("test executable")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
