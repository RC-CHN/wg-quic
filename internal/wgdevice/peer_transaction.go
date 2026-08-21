package wgdevice

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
)

type PeerMutationOperation string

const (
	PeerMutationAdd    PeerMutationOperation = "add"
	PeerMutationUpdate PeerMutationOperation = "update"
	PeerMutationRemove PeerMutationOperation = "remove"
)

type PeerMutation struct {
	Operation PeerMutationOperation
	Peer      config.Peer
}

type PreparedPeerSet interface {
	Commit() error
	Rollback() error
	Finalize() error
}

func PreparePeerSet(dev *device.Device, mutations []PeerMutation) (PreparedPeerSet, error) {
	if dev == nil {
		return nil, errors.New("WireGuard device is required")
	}
	converted := make([]device.PeerMutation, len(mutations))
	for index, mutation := range mutations {
		operation, err := convertPeerMutationOperation(mutation.Operation)
		if err != nil {
			return nil, fmt.Errorf("peer mutation %d: %w", index+1, err)
		}
		publicKey, err := decodePeerPublicKey(mutation.Peer.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("peer mutation %d public key: %w", index+1, err)
		}
		converted[index] = device.PeerMutation{
			Operation: operation,
			Peer: device.RuntimePeerConfig{
				PublicKey:           publicKey,
				AllowedIPs:          append([]netip.Prefix(nil), mutation.Peer.AllowedIPs...),
				PersistentKeepalive: mutation.Peer.PersistentKeepalive,
			},
		}
		if mutation.Peer.PresharedKey != "" {
			key, err := decodePresharedKey(mutation.Peer.PresharedKey)
			if err != nil {
				return nil, fmt.Errorf("peer mutation %d preshared key: %w", index+1, err)
			}
			converted[index].Peer.PresharedKey = key
		}
	}
	return dev.PreparePeerSet(converted)
}

func convertPeerMutationOperation(operation PeerMutationOperation) (device.PeerMutationOperation, error) {
	switch operation {
	case PeerMutationAdd:
		return device.PeerMutationAdd, nil
	case PeerMutationUpdate:
		return device.PeerMutationUpdate, nil
	case PeerMutationRemove:
		return device.PeerMutationRemove, nil
	default:
		return 0, fmt.Errorf("unsupported operation %q", operation)
	}
}

func decodePresharedKey(value string) (device.NoisePresharedKey, error) {
	var result device.NoisePresharedKey
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return result, err
	}
	if len(decoded) != len(result) {
		return result, fmt.Errorf("decoded length is %d, want %d", len(decoded), len(result))
	}
	copy(result[:], decoded)
	return result, nil
}
