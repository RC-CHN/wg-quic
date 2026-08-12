//go:build windows

package quick

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func platformValidateCoreExecutable(current string, path string) error {
	if err := validateWindowsTrustedRuntimeExecutable(
		path,
		coreExecutableName(),
	); err == nil {
		return nil
	}
	currentInstalled := validateWindowsTrustedInstalledFile(current) == nil
	if currentInstalled {
		return validateWindowsTrustedInstalledFile(path)
	}
	privileged, err := windowsPrivilegedExecutableLocation(current)
	if err != nil {
		return err
	}
	if privileged {
		return fmt.Errorf(
			"privileged quick executable %q failed provenance validation",
			current,
		)
	}
	managerBundleErr := validateWindowsTrustedManagerBundleFile(current, path)
	if managerBundleErr == nil {
		return nil
	}
	if strings.EqualFold(
		filepath.Base(current),
		windowsManagementServiceName+".exe",
	) {
		return managerBundleErr
	}
	if !strings.EqualFold(filepath.Dir(current), filepath.Dir(path)) ||
		!strings.EqualFold(filepath.Base(path), coreExecutableName()) {
		return fmt.Errorf("portable Windows core must be the quick executable sibling")
	}
	file, err := openWindowsReadOnlyNoFollowFile(path)
	if err != nil {
		return fmt.Errorf("open portable Windows core: %w", err)
	}
	return file.Close()
}

// validateWindowsTrustedManagerBundleFile supports a per-machine MSI rooted
// outside Program Files without treating every executable in that directory as
// trusted. The running service image is the anchor: only wg-quic-manager.exe
// may select the fixed bin\wg-quic.exe child, and every object from the
// application root down must already have an installer-trusted owner and no
// mutation rights for an untrusted SID.
//
// Holding no-share-delete handles for the root and bin directories also keeps
// a validated bundle pinned while the core file is opened. This preserves the
// same replace-resistance used for Program Files installations.
func validateWindowsTrustedManagerBundleFile(current string, path string) error {
	current = filepath.Clean(current)
	path = filepath.Clean(path)
	if !strings.EqualFold(
		filepath.Base(current),
		windowsManagementServiceName+".exe",
	) {
		return fmt.Errorf("custom installed bundle anchor is not wg-quic-manager.exe")
	}
	root := filepath.Dir(current)
	bin := filepath.Join(root, "bin")
	want := filepath.Join(bin, coreExecutableName())
	if !strings.EqualFold(path, want) {
		return fmt.Errorf(
			"custom installed manager core %q is not the fixed child %q",
			path,
			want,
		)
	}

	lease := &windowsSecurePathLease{}
	defer lease.Close()
	for _, directory := range []string{root, bin} {
		handle, err := openWindowsDirectoryNoFollow(
			directory,
			windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		)
		if err != nil {
			return fmt.Errorf(
				"open custom installed bundle directory %q: %w",
				directory,
				err,
			)
		}
		lease.append(handle)
		if err := inspectWindowsPathHandle(
			handle, directory, true, false,
		); err != nil {
			return err
		}
		if err := verifyWindowsInstalledPathSecurity(
			handle,
			"custom installed bundle directory",
		); err != nil {
			return err
		}
	}

	manager, err := openWindowsInstalledFileForValidation(
		current,
		"custom installed management service",
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(manager)
	core, err := openWindowsInstalledFileForValidation(
		path,
		"custom installed core executable",
	)
	if err != nil {
		return err
	}
	return windows.CloseHandle(core)
}

func windowsPrivilegedExecutableLocation(path string) (bool, error) {
	for _, folderID := range []*windows.KNOWNFOLDERID{
		windows.FOLDERID_ProgramFiles,
		windows.FOLDERID_ProgramData,
	} {
		root, err := windows.KnownFolderPath(folderID, windows.KF_FLAG_DEFAULT)
		if err != nil {
			return false, err
		}
		relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
		if err == nil && relative != "." && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}
