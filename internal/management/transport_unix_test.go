//go:build !windows

package management

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixManagementEndpointIsRootOnlyAndClientRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg0.manage.sock")
	server, err := Start(context.Background(), path, HandlerFunc(func(_ context.Context, request Request) Response {
		return Response{Status: &Status{
			Interface:         request.Interface,
			SupervisorEpoch:   "epoch",
			DesiredGeneration: 1,
			Capabilities:      []string{"management_protocol_v1"},
		}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("management socket mode = %o, want 600", got)
	}
	status, err := NewClient(path).Status(context.Background(), "wg0")
	if err != nil {
		t.Fatal(err)
	}
	if status.Interface != "wg0" || status.SupervisorEpoch != "epoch" {
		t.Fatalf("status = %#v", status)
	}
}
