package control

import (
	"context"
	"testing"
)

func TestStatusServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := testControlPath(t, "wg0")
	server, err := Start(ctx, path, func() Status {
		return Status{Interface: "wg0", State: "up", ListenPort: 443, Carrier: "quic", FECMode: "auto", ObfsMode: "salamander"}
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Interface != "wg0" || status.ListenPort != 443 || status.FECMode != "auto" || status.ObfsMode != "salamander" {
		t.Fatalf("unexpected status: %#v", status)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}
