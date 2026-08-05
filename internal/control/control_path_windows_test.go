//go:build windows

package control

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func testControlPath(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\wg-quic-test-%s-%d-%d`, name, os.Getpid(), time.Now().UnixNano())
}

func TestReadOnlyStatusEndpoint(t *testing.T) {
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
