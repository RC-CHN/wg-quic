package quick

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

func coreExecutable() (string, error) {
	current, err := os.Executable()
	if err == nil {
		sibling := filepath.Join(filepath.Dir(current), coreExecutableName())
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			return sibling, nil
		}
	}
	path, err := exec.LookPath(coreExecutableName())
	if err != nil {
		return "", errors.New("cannot find the wg-quic core executable next to wg-quic-quick or in PATH")
	}
	return path, nil
}
