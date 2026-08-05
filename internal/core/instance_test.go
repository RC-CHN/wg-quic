package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
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
	if len(status.Peers) != 1 || status.Peers[0].Endpoint != update.Endpoint || status.Peers[0].Generation != 2 {
		t.Fatalf("peer status = %#v, want endpoint update %#v", status.Peers, update)
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
