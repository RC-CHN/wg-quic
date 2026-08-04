package quick

import (
	"context"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
)

func TestLocalCoreEndpointControlWaitsForExactReadyGeneration(t *testing.T) {
	path := testControlPath(t.TempDir(), "wg0")
	update := endpoint.PeerUpdate{
		PublicKey: "peer", Endpoint: mustAddrPort(t, "192.0.2.10:443"), Generation: 4,
	}
	server, err := control.Start(context.Background(), path, func() control.Status {
		return control.Status{
			Interface: "wg0", State: "up",
			Peers: []control.PeerStatus{{
				PublicKey: update.PublicKey, Endpoint: update.Endpoint.String(),
				Generation: update.Generation, Session: "established",
			}},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := localCoreEndpointControl{client: control.NewClient(path)}
	if err := client.WaitPeerReady(context.Background(), update); err != nil {
		t.Fatal(err)
	}
}
