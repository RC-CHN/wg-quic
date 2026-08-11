//go:build !windows

package quick

import "os"

func platformValidateCoreExecutable(_ string, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.ErrInvalid
	}
	return nil
}
