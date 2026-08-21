//go:build linux || freebsd

package quick

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSecureUnixTestConfig(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	contents := "[Interface]\nPrivateKey = " + testQuickKey(1) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenSecureUnixConfigSnapshotPinsProtectedPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "staging")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeSecureUnixTestConfig(t, directory, "wg0.conf")
	cfg, err := openSecureUnixConfigSnapshot(path, root, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interface.PrivateKey != testQuickKey(1) {
		t.Fatal("secure descriptor did not parse the expected snapshot")
	}
}

func TestOpenSecureUnixConfigSnapshotRejectsWritableAncestorAndSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "staging")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeSecureUnixTestConfig(t, directory, "wg0.conf")
	if err := os.Chmod(directory, 0o722); err != nil {
		t.Fatal(err)
	}
	if _, err := openSecureUnixConfigSnapshot(path, root, uint32(os.Geteuid())); err == nil ||
		!strings.Contains(err.Error(), "writable") {
		t.Fatalf("writable ancestor error = %v", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.conf")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openSecureUnixConfigSnapshot(link, root, uint32(os.Geteuid())); err == nil {
		t.Fatal("symlinked configuration was accepted")
	}
}

func TestOpenSecureUnixConfigSnapshotRejectsWrongOwnerAndOversize(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeSecureUnixTestConfig(t, root, "wg0.conf")
	wrongOwner := uint32(os.Geteuid()) + 1
	if _, err := openSecureUnixConfigSnapshot(path, root, wrongOwner); err == nil ||
		!strings.Contains(err.Error(), "owned by") {
		t.Fatalf("wrong-owner error = %v", err)
	}
	oversize := filepath.Join(root, "large.conf")
	file, err := os.OpenFile(oversize, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, maxSecureConfigBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openSecureUnixConfigSnapshot(oversize, root, uint32(os.Geteuid())); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error = %v", err)
	}
}
