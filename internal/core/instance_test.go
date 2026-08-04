package core

import (
	"bytes"
	"context"
	"encoding/base64"
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
}

func (h *testDeviceHost) ValidateInterfaceName(name string) error {
	return nil
}

func (h *testDeviceHost) ControlPath(name string) string {
	return h.controlPath
}

func (h *testDeviceHost) CreateTUN(name string, mtu int) (tun.Device, error) {
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
