//go:build !windows

package observe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func makeSecureOutputDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("observation output path already exists: %s", path)
		}
		return fmt.Errorf("create observation output directory: %w", err)
	}
	return os.Chmod(path, 0o700)
}

func openSecureOutputFile(directory, name string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create observation artifact %s: %w", name, err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("restrict observation artifact %s: %w", name, err)
	}
	return file, nil
}

func writeMarker(directory, name string) error {
	temporary := "." + name + ".tmp"
	file, err := openSecureOutputFile(directory, temporary)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(name + "\n"); err != nil {
		file.Close()
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(filepath.Join(directory, temporary), filepath.Join(directory, name))
}
