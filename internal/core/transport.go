package core

import (
	"encoding/base64"
	"fmt"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
)

type transportConfiguration struct {
	Bind     armorbind.Config
	PeerKeys map[string]obfs.Key
}

func buildTransportConfiguration(cfg *config.Config) (transportConfiguration, error) {
	result := transportConfiguration{
		Bind:     armorbind.DefaultConfig(),
		PeerKeys: make(map[string]obfs.Key, len(cfg.Peers)),
	}
	result.Bind.FECMode = cfg.Transport.FEC
	if cfg.Transport.FECDataShards > 0 {
		result.Bind.FECDataShards = cfg.Transport.FECDataShards
	}
	if cfg.Transport.FECInterleave > 0 {
		result.Bind.FECInterleave = cfg.Transport.FECInterleave
	}
	result.Bind.CongestionMode = cfg.Transport.Congestion
	result.Bind.ObfsMode = cfg.Transport.Obfs
	if result.Bind.ObfsMode == "none" {
		return result, nil
	}
	localPrivate, err := decodeWireGuardKey(cfg.Interface.PrivateKey)
	if err != nil {
		return result, fmt.Errorf("decode local WireGuard private key: %w", err)
	}
	for i, peer := range cfg.Peers {
		remotePublic, err := decodeWireGuardKey(peer.PublicKey)
		if err != nil {
			return result, fmt.Errorf("decode Peer %d WireGuard public key: %w", i+1, err)
		}
		var preshared []byte
		if peer.PresharedKey != "" {
			preshared, err = decodeWireGuardKey(peer.PresharedKey)
			if err != nil {
				return result, fmt.Errorf("decode Peer %d WireGuard preshared key: %w", i+1, err)
			}
		}
		key, err := obfs.DeriveWireGuardKey(localPrivate, remotePublic, preshared)
		if err != nil {
			return result, fmt.Errorf("derive Peer %d obfuscation key: %w", i+1, err)
		}
		result.Bind.ObfsKeys = append(result.Bind.ObfsKeys, key)
		result.PeerKeys[peer.PublicKey] = key
	}
	return result, nil
}

func decodeWireGuardKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("decoded length is %d, want 32", len(key))
	}
	return key, nil
}
