//go:build !windows

package quick

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestImportUnixDesktopConfigIsPrivateAndAtomic(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.conf")
	destination := filepath.Join(root, "configs", "office.conf")
	if err := os.WriteFile(source, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := importUnixDesktopConfig(source, destination, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("destination mode = %o, want 600", got)
	}
	if err := importUnixDesktopConfig(source, destination, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second import error = %v, want os.ErrExist", err)
	}
	if err := os.WriteFile(source, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := importUnixDesktopConfig(source, destination, true); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "second\n" {
		t.Fatalf("destination contents = %q", contents)
	}
}

func TestImportUnixDesktopConfigRejectsLargeSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.conf")
	if err := os.WriteFile(
		source,
		make([]byte, maxDesktopConfigSize+1),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := importUnixDesktopConfig(
		source,
		filepath.Join(root, "configs", "office.conf"),
		false,
	)
	if err == nil {
		t.Fatal("large source was accepted")
	}
}
