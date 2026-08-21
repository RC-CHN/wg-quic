package device

import "testing"

type sequencedDummyEndpoint struct {
	*DummyEndpoint
	sequence uint64
	session  uint64
}

func (e *sequencedDummyEndpoint) ReceiveSequence() uint64 { return e.sequence }
func (e *sequencedDummyEndpoint) SessionID() uint64       { return e.session }

func TestNotifyAuthenticatedReceiveIncludesPeerEndpointAndIngressSequence(t *testing.T) {
	endpoint, err := CreateDummyEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	var publicKey NoisePublicKey
	publicKey[0] = 42
	peer := &Peer{}
	peer.handshake.remoteStatic = publicKey
	var received AuthenticatedReceive
	device := &Device{authenticatedReceive: func(event AuthenticatedReceive) {
		received = event
	}}
	device.notifyAuthenticatedReceive(peer, &sequencedDummyEndpoint{
		DummyEndpoint: endpoint, sequence: 17, session: 23,
	})
	if received.PublicKey != publicKey || received.Endpoint != endpoint.DstToString() ||
		received.ReceiveSequence != 17 || received.SessionID != 23 {
		t.Fatalf("authenticated receive = %#v", received)
	}
}
