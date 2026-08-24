package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
	"github.com/RC-CHN/wg-quic/internal/wgdevice"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/tuntest"
	"golang.org/x/crypto/curve25519"
)

type testDeviceHost struct {
	tunnel      *tuntest.ChannelTUN
	controlPath string
	createdMTU  int
}

func (h *testDeviceHost) ValidateInterfaceName(name string) error {
	return nil
}

func (h *testDeviceHost) ControlPath(name string) string {
	return h.controlPath
}

func (h *testDeviceHost) CreateTUN(name string, mtu int) (tun.Device, error) {
	h.createdMTU = mtu
	return h.tunnel.TUN(), nil
}

func TestInstanceLifecycleNeedsOnlyDeviceHost(t *testing.T) {
	host := &testDeviceHost{
		tunnel:      tuntest.NewChannelTUN(),
		controlPath: testControlPath(t, "wg0"),
	}
	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
		Transport: config.DefaultTransport(),
	}
	instance, err := New(cfg, "wg0", host)
	if err != nil {
		t.Fatal(err)
	}
	if host.createdMTU != config.DefaultMTU {
		t.Fatalf("CreateTUN MTU = %d, want %d", host.createdMTU, config.DefaultMTU)
	}
	if err := instance.Up(context.Background()); err != nil {
		instance.Close()
		t.Fatal(err)
	}
	status, err := control.Read(host.controlPath)
	if err != nil {
		instance.Close()
		t.Fatal(err)
	}
	if status.Interface != "wg0" || status.Carrier != "quic" || status.ObfsMode != "salamander" {
		t.Fatalf("core status = %#v", status)
	}
	if status.Stats.RuntimeAllocBytes == 0 || status.Stats.RuntimeAllocObjects == 0 {
		t.Fatalf("runtime allocation telemetry is empty: %#v", status.Stats)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wait := make(chan error, 1)
	go func() {
		wait <- instance.Wait(ctx)
	}()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("core instance did not stop waiting after cancellation")
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLatestPeerActivityPreservesDirection(t *testing.T) {
	for _, test := range []struct {
		lastRx        int64
		lastTx        int64
		wantTimestamp int64
		wantDirection string
	}{
		{lastRx: 0, lastTx: 0},
		{lastRx: 10, lastTx: 9, wantTimestamp: 10, wantDirection: "received"},
		{lastRx: 10, lastTx: 10, wantTimestamp: 10, wantDirection: "received"},
		{lastRx: 9, lastTx: 10, wantTimestamp: 10, wantDirection: "sent"},
	} {
		gotTimestamp, gotDirection := latestPeerActivity(test.lastRx, test.lastTx)
		if gotTimestamp != test.wantTimestamp || gotDirection != test.wantDirection {
			t.Fatalf(
				"latestPeerActivity(%d, %d) = (%d, %q), want (%d, %q)",
				test.lastRx,
				test.lastTx,
				gotTimestamp,
				gotDirection,
				test.wantTimestamp,
				test.wantDirection,
			)
		}
	}
}

func TestAssociateConfiguredSessionPeersPreservesManyToOneAuthentication(t *testing.T) {
	sessions := []telemetry.SessionObservation{{
		ConfiguredEndpoint: "192.0.2.10:443",
		Peers: []telemetry.SessionPeerObservation{{
			PublicKey: "peer-b", EndpointGeneration: 2, Authenticated: true,
		}},
	}}
	peers := []control.PeerStatus{
		{PublicKey: "peer-b", SelectedEndpoint: "192.0.2.10:443", Generation: 4},
		{PublicKey: "peer-a", SelectedEndpoint: "192.0.2.10:443", Generation: 5},
		{PublicKey: "peer-c", SelectedEndpoint: "198.51.100.20:443", Generation: 6},
	}
	associateConfiguredSessionPeers(sessions, peers)
	if len(sessions[0].Peers) != 2 {
		t.Fatalf("session peer associations = %#v", sessions[0].Peers)
	}
	first, second := sessions[0].Peers[0], sessions[0].Peers[1]
	if first.PublicKey != "peer-a" || !first.Configured || first.Authenticated ||
		first.EndpointGeneration != 5 {
		t.Fatalf("first session peer = %#v", first)
	}
	if second.PublicKey != "peer-b" || !second.Configured || !second.Authenticated ||
		second.EndpointGeneration != 4 {
		t.Fatalf("second session peer = %#v", second)
	}
}

func TestPeerSessionEndpointUsesLiveWireGuardPath(t *testing.T) {
	selected := "192.0.2.10:52820"
	live := "198.51.100.20:52821"
	for _, test := range []struct {
		name string
		peer control.PeerStatus
		want string
		ok   bool
	}{
		{
			name: "inbound live endpoint without selected endpoint",
			peer: control.PeerStatus{Endpoint: live},
			want: live,
			ok:   true,
		},
		{
			name: "roamed live endpoint wins over selected endpoint",
			peer: control.PeerStatus{Endpoint: live, SelectedEndpoint: selected},
			want: live,
			ok:   true,
		},
		{
			name: "selected endpoint is the pre-handshake fallback",
			peer: control.PeerStatus{SelectedEndpoint: selected},
			want: selected,
			ok:   true,
		},
		{
			name: "invalid live endpoint falls back to selected endpoint",
			peer: control.PeerStatus{Endpoint: "vpn.example.test:52820", SelectedEndpoint: selected},
			want: selected,
			ok:   true,
		},
		{name: "peer without an endpoint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := peerSessionEndpoint(test.peer)
			if ok != test.ok || (ok && got.String() != test.want) {
				t.Fatalf("peerSessionEndpoint(%#v) = (%q, %t), want (%q, %t)", test.peer, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestInstancePreparedStateDefersNetworkActivation(t *testing.T) {
	host := &testDeviceHost{
		tunnel:      tuntest.NewChannelTUN(),
		controlPath: testControlPath(t, "wg0"),
	}
	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
		Transport: config.DefaultTransport(),
	}
	instance, err := New(cfg, "wg0", host)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := control.NewClient(host.controlPath)
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "prepared" || status.ListenPort != 0 {
		t.Fatalf("prepared core status = %#v", status)
	}
	if err := client.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := client.Activate(); err != nil {
		t.Fatalf("idempotent activate failed: %v", err)
	}
	status, err = client.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "up" || status.ListenPort == 0 {
		t.Fatalf("activated core status = %#v", status)
	}
}

func TestInstanceUpdatesNumericPeerEndpointThroughControl(t *testing.T) {
	privateKey := bytes.Repeat([]byte{0x31}, 32)
	remotePrivate := bytes.Repeat([]byte{0x42}, 32)
	remotePublic, err := curve25519.X25519(remotePrivate, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.StdEncoding.EncodeToString(remotePublic)
	host := &testDeviceHost{
		tunnel:      tuntest.NewChannelTUN(),
		controlPath: testControlPath(t, "wg0"),
	}
	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
		},
		Peers: []config.Peer{{
			PublicKey: publicKey,
			Endpoint:  "192.0.2.1:443",
		}},
		Transport: config.DefaultTransport(),
	}
	instance, err := New(cfg, "wg0", host)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	initial := instance.status()
	if len(initial.Peers) != 1 ||
		initial.Peers[0].Endpoint != "192.0.2.1:443" ||
		initial.Peers[0].SelectedEndpoint != "192.0.2.1:443" ||
		initial.Peers[0].Generation != 1 {
		t.Fatalf("initial peer endpoint did not use the runtime activation path: %#v", initial.Peers)
	}
	if err := instance.Up(context.Background()); err != nil {
		t.Fatal(err)
	}

	update := control.SetPeerEndpointRequest{
		PublicKey: publicKey, Endpoint: "[2001:db8::20]:8443", Generation: 2,
	}
	if err := control.SetPeerEndpoint(host.controlPath, update); err != nil {
		t.Fatal(err)
	}
	status, err := control.Read(host.controlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Peers) != 1 || status.Peers[0].Endpoint != update.Endpoint ||
		status.Peers[0].SelectedEndpoint != update.Endpoint || status.Peers[0].Generation != 2 {
		t.Fatalf("peer status = %#v, want endpoint update %#v", status.Peers, update)
	}
	var authenticatedPublic device.NoisePublicKey
	copy(authenticatedPublic[:], remotePublic)
	instance.recordAuthenticatedReceive(device.AuthenticatedReceive{
		PublicKey: authenticatedPublic, Endpoint: update.Endpoint, ReceiveSequence: 1,
	})
	status = instance.status()
	if status.Peers[0].AuthenticatedGeneration != update.Generation ||
		status.Peers[0].AuthenticatedEndpoint != update.Endpoint {
		t.Fatalf("authenticated endpoint generation = %#v", status.Peers[0])
	}
	roamed := netip.MustParseAddrPort("[2001:db8::99]:9443")
	if err := wgdevice.SetPeerEndpoint(instance.device, publicKey, roamed); err != nil {
		t.Fatal(err)
	}
	status = instance.status()
	if status.Peers[0].Endpoint != roamed.String() ||
		status.Peers[0].SelectedEndpoint != update.Endpoint ||
		status.Peers[0].Generation != update.Generation {
		t.Fatalf("roamed peer status confused current and selected endpoints: %#v", status.Peers[0])
	}
	if err := wgdevice.SetPeerEndpoint(
		instance.device,
		publicKey,
		netip.MustParseAddrPort(update.Endpoint),
	); err != nil {
		t.Fatal(err)
	}
	uapi, err := instance.device.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uapi, "endpoint="+update.Endpoint+"\n") {
		t.Fatalf("WireGuard UAPI did not contain updated endpoint:\n%s", uapi)
	}
	if err := control.SetPeerEndpoint(host.controlPath, update); err != nil {
		t.Fatalf("idempotent update failed: %v", err)
	}
	stale := update
	stale.Generation = 1
	if err := control.SetPeerEndpoint(host.controlPath, stale); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale update error = %v, want stale generation rejection", err)
	}
	conflict := update
	conflict.Endpoint = "192.0.2.99:443"
	if err := control.SetPeerEndpoint(host.controlPath, conflict); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting update error = %v, want generation conflict", err)
	}
	next := update
	next.Endpoint = "192.0.2.100:443"
	next.Generation = 3
	if err := control.SetPeerEndpoint(host.controlPath, next); err != nil {
		t.Fatal(err)
	}
	if got := len(instance.peers[publicKey].obsoleteEndpoints); got != 2 {
		t.Fatalf("obsolete endpoint resources = %d, want 2 before finalization", got)
	}
	if err := control.NewClient(host.controlPath).FinalizePeerEndpoint(
		publicKey, next.Generation,
	); err != nil {
		t.Fatal(err)
	}
	if got := len(instance.peers[publicKey].obsoleteEndpoints); got != 0 {
		t.Fatalf("obsolete endpoint resources after finalization = %d", got)
	}
	instance.recordAuthenticatedReceive(device.AuthenticatedReceive{
		PublicKey: authenticatedPublic, Endpoint: update.Endpoint, ReceiveSequence: 2,
	})
	status = instance.status()
	if status.Peers[0].AuthenticatedGeneration != 0 || status.Peers[0].AuthenticatedEndpoint != "" {
		t.Fatalf("old authenticated endpoint satisfied a new generation: %#v", status.Peers[0])
	}
}

type noTUNHost struct {
	createCalled bool
}

func (*noTUNHost) ValidateInterfaceName(string) error { return nil }
func (*noTUNHost) ControlPath(string) string          { return "" }
func (h *noTUNHost) CreateTUN(string, int) (tun.Device, error) {
	h.createCalled = true
	return nil, errors.New("CreateTUN must not be called")
}

func TestCoreRejectsHostnameEndpointBeforeCreatingTUN(t *testing.T) {
	host := &noTUNHost{}
	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
		Peers: []config.Peer{{
			PublicKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
			Endpoint:  "vpn.example.test:443",
		}},
		Transport: config.DefaultTransport(),
	}
	_, err := New(cfg, "wg0", host)
	if err == nil || !strings.Contains(err.Error(), "run wg-quic-quick") {
		t.Fatalf("hostname endpoint error = %v", err)
	}
	if host.createCalled {
		t.Fatal("core created TUN before rejecting its unsupported hostname endpoint")
	}
}
