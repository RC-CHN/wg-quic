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
