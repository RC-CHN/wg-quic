//go:build !windows

package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func testControlPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name+".sock")
}

func TestReadOnlyStatusEndpointIsWorldReadableAndStatusOnly(t *testing.T) {
	path := testControlPath(t, "status")
	server, err := StartReadOnlyStatus(
		context.Background(),
		path,
		func() Status {
			return Status{Interface: "status", State: "up"}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})

	info, err := os.Stat(readOnlyStatusPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o666 {
		t.Fatalf("read-only status socket mode = %o, want 666", got)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("status socket directory mode = %o, want 755", got)
	}
	status, err := NewClient(path).Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Interface != "status" || status.State != "up" {
		t.Fatalf("Status() = %#v", status)
	}
	client := &LocalClient{path: path}
	var resp response
	if err := client.callAt(
		readOnlyStatusPath(path),
		request{Operation: "activate"},
		&resp,
	); err == nil {
		t.Fatal("read-only status endpoint unexpectedly accepted Activate")
	}
}
