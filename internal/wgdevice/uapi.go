package wgdevice

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
)

func Configure(dev *device.Device, cfg *config.Config) error {
	uapi, err := cfg.UAPI()
	if err != nil {
		return err
	}
	return dev.IpcSet(uapi)
}

// SetPeerEndpoint performs the one canonical WireGuard UAPI update used by
// every management frontend. Callers retain ownership of DNS and route policy.
func SetPeerEndpoint(dev *device.Device, publicKey string, endpoint netip.AddrPort) error {
	key, err := base64.StdEncoding.Strict().DecodeString(publicKey)
	if err != nil {
		return fmt.Errorf("decode peer public key: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("decode peer public key: got %d bytes, want 32", len(key))
	}
	if !endpoint.IsValid() || endpoint.Port() == 0 {
		return fmt.Errorf("valid numeric peer endpoint is required")
	}
	uapi := fmt.Sprintf(
		"public_key=%s\nupdate_only=true\nendpoint=%s\n",
		hex.EncodeToString(key), endpoint,
	)
	return dev.IpcSet(uapi)
}
