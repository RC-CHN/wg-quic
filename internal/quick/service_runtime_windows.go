//go:build windows

package quick

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const windowsServiceRuntimeDirectory = "runtime"

type windowsRuntimeSource struct {
	name string
	path string
}

// prepareWindowsServiceRuntime moves the tunnel service boundary out of an
// updater-owned application directory. A fresh, unpredictable SYSTEM-owned
// directory is created for every start. We intentionally do not reuse the old
// predictable content-digest directory: an attacker could have pre-positioned
// that pathname before an upgrade and retain a write handle after an ACL fix.
// Old runtime directories remain in place so already-running tunnel services
// survive desktop upgrades and uninstall operations.
func prepareWindowsServiceRuntime(
	quickExecutable string,
) (runtimeExecutable string, runtimeLease *windowsSecurePathLease, returnErr error) {
	core, err := coreExecutable()
	if err != nil {
		return "", nil, err
	}
	sources := []windowsRuntimeSource{
		{name: "wg-quic-quick.exe", path: quickExecutable},
		{name: "wg-quic.exe", path: core},
		{
			name: "wintun.dll",
			path: filepath.Join(filepath.Dir(core), "wintun.dll"),
		},
	}

	root, lease, err := openWindowsSecureRuntimeRoot()
	if err != nil {
		return "", nil, err
	}
	target := ""
	installed := false
	defer func() {
		if !installed {
			_ = lease.Close()
			if target != "" {
				_ = removeWindowsTrustedRuntimeDirectory(target)
			}
		}
	}()

	target, targetHandle, err := createWindowsSecureChildDirectory(
		root,
		"run-",
	)
	if err != nil {
		return "", nil, fmt.Errorf("create secure Windows service runtime: %w", err)
	}
	lease.append(targetHandle)

	for _, source := range sources {
		destination := filepath.Join(target, source.name)
		if err := copyWindowsRuntimeFileSecure(
			source.path,
			destination,
		); err != nil {
			return "", nil, fmt.Errorf(
				"stage Windows service runtime %s: %w",
				source.name,
				err,
			)
		}
	}
	installed = true
	return filepath.Join(target, "wg-quic-quick.exe"), lease, nil
}

func copyWindowsRuntimeFileSecure(
	source string,
	destination string,
) (returnErr error) {
	input, err := openWindowsReadOnlyNoFollowFile(source)
	if err != nil {
		return fmt.Errorf("open runtime source without following reparses: %w", err)
	}
	defer input.Close()

	output, err := createWindowsSecureFile(
		destination,
		"service runtime file",
	)
	if err != nil {
		return fmt.Errorf("create secure runtime destination: %w", err)
	}
	keepDestination := false
	defer func() {
		closeErr := output.Close()
		if !keepDestination {
			_ = os.Remove(destination)
		}
		if returnErr == nil && closeErr != nil {
			returnErr = fmt.Errorf("close runtime destination: %w", closeErr)
		}
	}()

	sourceHash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, sourceHash), input); err != nil {
		return fmt.Errorf("copy runtime file: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("flush runtime file: %w", err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind runtime file for verification: %w", err)
	}
	targetHash := sha256.New()
	if _, err := io.Copy(targetHash, output); err != nil {
		return fmt.Errorf("verify runtime file: %w", err)
	}
	if string(sourceHash.Sum(nil)) != string(targetHash.Sum(nil)) {
		return fmt.Errorf("runtime file failed content verification")
	}
	if err := inspectWindowsPathHandle(
		windowsHandleFromFile(output),
		destination,
		false,
		true,
	); err != nil {
		return err
	}
	keepDestination = true
	return nil
}

func windowsHandleFromFile(file *os.File) windows.Handle {
	return windows.Handle(file.Fd())
}
