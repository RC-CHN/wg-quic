//go:build windows

package observe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const observationDirectoryDACL = "D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)"

func makeSecureOutputDir(path string) error {
	descriptor, err := windows.SecurityDescriptorFromString(observationDirectoryDACL)
	if err != nil {
		return fmt.Errorf("build observation output ACL: %w", err)
	}
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	err = windows.CreateDirectory(pathUTF16, attributes)
	runtime.KeepAlive(descriptor)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("observation output path already exists: %s", path)
	}
	if err != nil {
		return fmt.Errorf("create protected observation output directory: %w", err)
	}
	return nil
}

func openSecureOutputFile(directory, name string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create observation artifact %s: %w", name, err)
	}
	return file, nil
}

func writeMarker(directory, name string) error {
	temporary := "." + name + ".tmp"
	file, err := openSecureOutputFile(directory, temporary)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(name + "\r\n"); err != nil {
		file.Close()
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(filepath.Join(directory, temporary), filepath.Join(directory, name))
}
