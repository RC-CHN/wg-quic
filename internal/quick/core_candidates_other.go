//go:build !windows

package quick

import (
	"os/exec"
	"path/filepath"
)

func platformCoreExecutableCandidates(current string) []string {
	return []string{
		filepath.Join(filepath.Dir(current), coreExecutableName()),
	}
}

func platformCoreExecutableFallback() (string, error) {
	return exec.LookPath(coreExecutableName())
}
