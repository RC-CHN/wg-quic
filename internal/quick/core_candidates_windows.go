//go:build windows

package quick

import (
	"errors"
	"path/filepath"
)

func platformCoreExecutableCandidates(current string) []string {
	directory := filepath.Dir(current)
	return []string{
		filepath.Join(directory, coreExecutableName()),
		// The MSI-owned management service is installed as
		// [INSTALLDIR]\wg-quic-manager.exe while native resources remain in
		// [INSTALLDIR]\bin. Its content is the same quick executable.
		filepath.Join(directory, "bin", coreExecutableName()),
	}
}

func platformCoreExecutableFallback() (string, error) {
	return "", errors.New("Windows core PATH fallback is disabled")
}
