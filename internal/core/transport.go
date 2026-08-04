package core

import (
	"encoding/base64"
	"fmt"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
)

func transportBindConfig(cfg *config.Config) (armorbind.Config, error) {
	result := armorbind.DefaultConfig()
	result.FECMode = cfg.Transport.FEC
	result.ObfsMode = cfg.Transport.Obfs
	if result.ObfsMode == "none" {
		return result, nil
	}
	localPrivate, err := decodeWireGuardKey(cfg.Interface.PrivateKey)
	if err != nil {
		return result, fmt.Errorf("decode local WireGuard private key: %w", err)
	}
	result.ObfsEndpointKeys = make(map[string]obfs.Key)
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
		result.ObfsKeys = append(result.ObfsKeys, key)
		if peer.Endpoint != "" {
			if existing, ok := result.ObfsEndpointKeys[peer.Endpoint]; ok && existing != key {
				return result, fmt.Errorf("Peer %d reuses endpoint %q with a different obfuscation key", i+1, peer.Endpoint)
			}
			result.ObfsEndpointKeys[peer.Endpoint] = key
		}
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
