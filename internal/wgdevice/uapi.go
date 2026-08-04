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
	key, err := decodePeerPublicKey(publicKey)
	if err != nil {
		return err
	}
	if !endpoint.IsValid() || endpoint.Port() == 0 {
		return fmt.Errorf("valid numeric peer endpoint is required")
	}
	uapi := fmt.Sprintf(
		"public_key=%s\nupdate_only=true\nendpoint=%s\n",
		hex.EncodeToString(key[:]), endpoint,
	)
	return dev.IpcSet(uapi)
}

func ProbePeer(dev *device.Device, publicKey string) error {
	key, err := decodePeerPublicKey(publicKey)
	if err != nil {
		return err
	}
	return dev.ProbePeer(key)
}

func decodePeerPublicKey(value string) (device.NoisePublicKey, error) {
	var result device.NoisePublicKey
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return result, fmt.Errorf("decode peer public key: %w", err)
	}
	if len(key) != len(result) {
		return result, fmt.Errorf("decode peer public key: got %d bytes, want %d", len(key), len(result))
	}
	copy(result[:], key)
	return result, nil
}
