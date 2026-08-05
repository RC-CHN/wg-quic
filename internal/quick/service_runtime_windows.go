//go:build windows

package quick

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const windowsServiceRuntimeDirectory = "runtime"

type windowsRuntimeSource struct {
	name string
	path string
}

// prepareWindowsServiceRuntime moves the service boundary out of an
// updater-owned per-user application directory. Each distinct native bundle
// gets an immutable content-addressed directory under ProgramData, so an
// active tunnel remains restartable after a desktop update.
func prepareWindowsServiceRuntime(
	quickExecutable string,
) (string, error) {
	core, err := coreExecutable()
	if err != nil {
		return "", err
	}
	sources := []windowsRuntimeSource{
		{name: "wg-quic-quick.exe", path: quickExecutable},
		{name: "wg-quic.exe", path: core},
		{
			name: "wintun.dll",
			path: filepath.Join(filepath.Dir(core), "wintun.dll"),
		},
	}
	digest, err := windowsRuntimeDigest(sources)
	if err != nil {
		return "", err
	}
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	root := filepath.Join(
		programData,
		"wg-quic",
		windowsServiceRuntimeDirectory,
	)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create Windows service runtime root: %w", err)
	}
	if err := protectWindowsDesktopPath(
		root,
		"D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
		"service runtime root",
	); err != nil {
		return "", err
	}
	target := filepath.Join(root, hex.EncodeToString(digest[:]))
	if err := ensureWindowsRuntimeDirectory(target, sources); err != nil {
		return "", err
	}
	return filepath.Join(target, "wg-quic-quick.exe"), nil
}

func windowsRuntimeDigest(
	sources []windowsRuntimeSource,
) ([sha256.Size]byte, error) {
	hash := sha256.New()
	for _, source := range sources {
		if _, err := io.WriteString(hash, source.name+"\x00"); err != nil {
			return [sha256.Size]byte{}, err
		}
		fileDigest, err := digestWindowsRuntimeFile(source.path)
		if err != nil {
			return [sha256.Size]byte{}, fmt.Errorf(
				"hash Windows runtime source %s: %w",
				source.name,
				err,
			)
		}
		if _, err := hash.Write(fileDigest[:]); err != nil {
			return [sha256.Size]byte{}, err
		}
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func ensureWindowsRuntimeDirectory(
	target string,
	sources []windowsRuntimeSource,
) error {
	if info, err := os.Stat(target); err == nil {
		if !info.IsDir() {
			return fmt.Errorf(
				"Windows service runtime target %q is not a directory",
				target,
			)
		}
		return verifyWindowsRuntimeDirectory(target, sources)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	temporary, err := os.MkdirTemp(
		filepath.Dir(target),
		"."+filepath.Base(target)+".tmp-",
	)
	if err != nil {
		return fmt.Errorf("create temporary Windows service runtime: %w", err)
	}
	keepTemporary := true
	defer func() {
		if keepTemporary {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, source := range sources {
		destination := filepath.Join(temporary, source.name)
		if err := copyWindowsRuntimeFile(source.path, destination); err != nil {
			return err
		}
		if err := protectWindowsDesktopPath(
			destination,
			"D:P(A;;FA;;;SY)(A;;FA;;;BA)",
			"service runtime file",
		); err != nil {
			return err
		}
	}
	if err := protectWindowsDesktopPath(
		temporary,
		"D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
		"service runtime directory",
	); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
			return verifyWindowsRuntimeDirectory(target, sources)
		}
		return fmt.Errorf("install Windows service runtime: %w", err)
	}
	keepTemporary = false
	return verifyWindowsRuntimeDirectory(target, sources)
}

func copyWindowsRuntimeFile(source string, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(
		destination,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o700,
	)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func verifyWindowsRuntimeDirectory(
	target string,
	sources []windowsRuntimeSource,
) error {
	for _, source := range sources {
		sourceDigest, err := digestWindowsRuntimeFile(source.path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(target, source.name)
		targetDigest, err := digestWindowsRuntimeFile(targetPath)
		if err != nil {
			return fmt.Errorf(
				"verify Windows service runtime %s: %w",
				source.name,
				err,
			)
		}
		if sourceDigest != targetDigest {
			return fmt.Errorf(
				"Windows service runtime %s failed content verification",
				source.name,
			)
		}
	}
	return nil
}

func digestWindowsRuntimeFile(
	path string,
) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}
