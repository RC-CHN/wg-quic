package core

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
	"golang.org/x/crypto/curve25519"
)

func TestTransportConfigDerivesMatchingKeysFromWireGuardConfig(t *testing.T) {
	privateA := bytes.Repeat([]byte{0x11}, 32)
	privateB := bytes.Repeat([]byte{0x22}, 32)
	publicA, err := curve25519.X25519(privateA, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	publicB, err := curve25519.X25519(privateB, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	psk := bytes.Repeat([]byte{0x33}, 32)
	a := &config.Config{
		Interface: config.Interface{PrivateKey: encodeTestKey(privateA)},
		Peers: []config.Peer{{
			PublicKey: encodeTestKey(publicB), PresharedKey: encodeTestKey(psk),
			Endpoint: "192.0.2.2:443",
		}},
		Transport: config.DefaultTransport(),
	}
	b := &config.Config{
		Interface: config.Interface{PrivateKey: encodeTestKey(privateB)},
		Peers: []config.Peer{{
			PublicKey: encodeTestKey(publicA), PresharedKey: encodeTestKey(psk),
		}},
		Transport: config.DefaultTransport(),
	}
	configA, err := transportBindConfig(a)
	if err != nil {
		t.Fatal(err)
	}
	configB, err := transportBindConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(configA.ObfsKeys) != 1 || len(configB.ObfsKeys) != 1 || configA.ObfsKeys[0] != configB.ObfsKeys[0] {
		t.Fatal("two WireGuard configurations did not derive the same transport key")
	}
	if configA.ObfsEndpointKeys["192.0.2.2:443"] != configA.ObfsKeys[0] {
		t.Fatal("configured WireGuard endpoint was not associated with its derived key")
	}
}

func encodeTestKey(value []byte) string {
	return base64.StdEncoding.EncodeToString(value)
}
