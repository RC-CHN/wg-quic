//go:build windows

package quick

import (
	"path/filepath"
	"testing"
)

func TestWindowsManagerFindsBundledCore(t *testing.T) {
	current := filepath.Join(`C:\Program Files\wg-quic`, "wg-quic-manager.exe")
	candidates := platformCoreExecutableCandidates(current)
	if len(candidates) != 2 {
		t.Fatalf("core candidate count = %d, want 2", len(candidates))
	}
	want := filepath.Join(`C:\Program Files\wg-quic`, "bin", "wg-quic.exe")
	if candidates[1] != want {
		t.Fatalf("manager core candidate = %q, want %q", candidates[1], want)
	}
}

func TestWindowsCoreExecutableDoesNotFallBackToPath(t *testing.T) {
	if path, err := platformCoreExecutableFallback(); err == nil {
		t.Fatalf("Windows core PATH fallback returned %q", path)
	}
}
