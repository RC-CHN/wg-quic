package control

import (
	"context"
	"errors"
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

func TestControlCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := testControlPath(t, "wg0")
	var got SetPeerEndpointRequest
	var redial string
	var activated bool
	server, err := StartHandler(ctx, path, Handler{
		Status: func() Status {
			return Status{Interface: "wg0", State: "up"}
		},
		SetPeerEndpoint: func(update SetPeerEndpointRequest) error {
			got = update
			return nil
		},
		RedialPeer: func(publicKey string) error {
			redial = publicKey
			return nil
		},
		Activate: func() error {
			activated = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	update := SetPeerEndpointRequest{
		PublicKey: "peer", Endpoint: "192.0.2.10:443", Generation: 7,
	}
	if err := SetPeerEndpoint(path, update); err != nil {
		t.Fatal(err)
	}
	if got != update {
		t.Fatalf("endpoint update = %#v, want %#v", got, update)
	}
	if err := RedialPeer(path, "peer"); err != nil {
		t.Fatal(err)
	}
	if redial != "peer" {
		t.Fatalf("redial peer = %q, want peer", redial)
	}
	if err := NewClient(path).Activate(); err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatal("activate handler was not called")
	}
}

func TestControlCommandReturnsHandlerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := testControlPath(t, "wg0")
	server, err := StartHandler(ctx, path, Handler{
		Status: func() Status { return Status{} },
		SetPeerEndpoint: func(SetPeerEndpointRequest) error {
			return errors.New("rejected")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	err = SetPeerEndpoint(path, SetPeerEndpointRequest{
		PublicKey: "peer", Endpoint: "192.0.2.10:443", Generation: 1,
	})
	if err == nil || err.Error() != "rejected" {
		t.Fatalf("command error = %v, want rejected", err)
	}
}
