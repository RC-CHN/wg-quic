//go:build windows

package quick

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateWindowsDebugLogUsesProgramData(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ProgramData", root)
	file, path, err := CreateWindowsDebugLog("wg0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	wantDirectory := filepath.Join(root, "wg-quic", "logs")
	if filepath.Dir(path) != wantDirectory {
		t.Fatalf("debug log directory = %q, want %q", filepath.Dir(path), wantDirectory)
	}
	if !strings.HasPrefix(filepath.Base(path), "wg0-debug-") || filepath.Ext(path) != ".log" {
		t.Fatalf("unexpected debug log name %q", filepath.Base(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Fatalf("debug log %q is a directory", path)
	}
}

func TestDebugPowerShellQuoteEscapesApostrophe(t *testing.T) {
	if got, want := debugPowerShellQuote("wg'o"), "'wg''o'"; got != want {
		t.Fatalf("quoted name = %q, want %q", got, want)
	}
}
