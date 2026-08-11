//go:build windows

package quick

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsCorePortableQuickAllowsOnlyNoFollowSibling(t *testing.T) {
	directory := t.TempDir()
	current := filepath.Join(directory, "wg-quic-quick.exe")
	core := filepath.Join(directory, coreExecutableName())
	if err := os.WriteFile(current, []byte("quick"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core, []byte("core"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := platformValidateCoreExecutable(current, core); err != nil {
		t.Fatalf("portable sibling core was rejected: %v", err)
	}
	otherDirectory := filepath.Join(directory, "other")
	if err := os.Mkdir(otherDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(otherDirectory, coreExecutableName())
	if err := os.WriteFile(other, []byte("other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := platformValidateCoreExecutable(current, other); err == nil {
		t.Fatal("portable quick accepted a non-sibling core")
	}
}
