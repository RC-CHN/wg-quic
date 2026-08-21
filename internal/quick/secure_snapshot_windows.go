//go:build windows

package quick

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func openSecureConfigSnapshot(path string) (*config.Config, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("secure Windows configuration path must be absolute")
	}
	interfaces, directoryLease, err := openWindowsSecureInterfacesDirectory()
	if err != nil {
		return nil, err
	}
	defer directoryLease.Close()
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve secure Windows configuration path: %w", err)
	}
	if !strings.EqualFold(filepath.Clean(filepath.Dir(path)), filepath.Clean(interfaces)) {
		return nil, fmt.Errorf(
			"Windows configuration %q is outside the protected interfaces directory",
			path,
		)
	}
	file, err := openWindowsSecureExistingFile(path, "runtime configuration snapshot")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseSecureConfigReader(file)
}
