package core

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/tuntest"
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
		controlPath: filepath.Join(t.TempDir(), "wg0.sock"),
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
