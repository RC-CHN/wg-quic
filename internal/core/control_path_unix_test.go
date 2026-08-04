//go:build !windows

package core

import (
	"path/filepath"
	"testing"
)

func testControlPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name+".sock")
}
