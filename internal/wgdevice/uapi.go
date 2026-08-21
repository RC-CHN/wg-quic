package wgdevice

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
)

// PeerStatus is the non-secret subset of WireGuard's live UAPI peer state.
type PeerStatus struct {
	PublicKey       string
	Endpoint        string
	LatestHandshake int64
	LastRx          int64
	LastTx          int64
	TransferRx      uint64
	TransferTx      uint64
}

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

func ClearPeerEndpoint(dev *device.Device, publicKey string) error {
	key, err := decodePeerPublicKey(publicKey)
	if err != nil {
		return err
	}
	return dev.ClearPeerEndpoint(key)
}

func ProbePeer(dev *device.Device, publicKey string) error {
	key, err := decodePeerPublicKey(publicKey)
	if err != nil {
		return err
	}
	return dev.ProbePeer(key)
}

// PeerStatuses reads the embedded WireGuard device and discards private and
// preshared keys before returning any state to the wg-quic control plane.
func PeerStatuses(dev *device.Device) (map[string]PeerStatus, error) {
	uapi, err := dev.IpcGet()
	if err != nil {
		return nil, err
	}
	return parsePeerStatuses(uapi)
}

func parsePeerStatuses(uapi string) (map[string]PeerStatus, error) {
	statuses := make(map[string]PeerStatus)
	var current *PeerStatus
	scanner := bufio.NewScanner(strings.NewReader(uapi))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			return nil, fmt.Errorf("parse WireGuard UAPI status line")
		}
		if key == "public_key" {
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != 32 {
				return nil, fmt.Errorf("parse WireGuard peer public key")
			}
			publicKey := base64.StdEncoding.EncodeToString(decoded)
			statuses[publicKey] = PeerStatus{PublicKey: publicKey}
			status := statuses[publicKey]
			current = &status
			continue
		}
		if current == nil {
			continue
		}
		switch key {
		case "endpoint":
			current.Endpoint = value
		case "last_handshake_time_sec":
			seconds, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse WireGuard peer handshake time: %w", err)
			}
			current.LatestHandshake = seconds
		case "last_authenticated_rx_time_sec":
			seconds, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse WireGuard peer receive time: %w", err)
			}
			current.LastRx = seconds
		case "last_authenticated_tx_time_sec":
			seconds, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse WireGuard peer transmit time: %w", err)
			}
			current.LastTx = seconds
		case "rx_bytes":
			bytes, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse WireGuard peer received bytes: %w", err)
			}
			current.TransferRx = bytes
		case "tx_bytes":
			bytes, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse WireGuard peer transmitted bytes: %w", err)
			}
			current.TransferTx = bytes
		}
		statuses[current.PublicKey] = *current
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read WireGuard UAPI status: %w", err)
	}
	return statuses, nil
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
