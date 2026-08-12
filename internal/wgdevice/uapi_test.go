package wgdevice

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParsePeerStatusesReturnsOnlyRuntimePeerState(t *testing.T) {
	privateKey := strings.Repeat("11", 32)
	publicKeyHex := strings.Repeat("22", 32)
	presharedKey := strings.Repeat("33", 32)
	uapi := strings.Join([]string{
		"private_key=" + privateKey,
		"listen_port=51820",
		"public_key=" + publicKeyHex,
		"preshared_key=" + presharedKey,
		"protocol_version=1",
		"endpoint=[2001:db8::10]:443",
		"last_handshake_time_sec=1785922000",
		"last_handshake_time_nsec=123",
		"last_authenticated_rx_time_sec=1785922001",
		"last_authenticated_tx_time_sec=1785922002",
		"tx_bytes=4567",
		"rx_bytes=8910",
		"persistent_keepalive_interval=25",
		"allowed_ip=10.97.0.2/32",
	}, "\n") + "\n"

	statuses, err := parsePeerStatuses(uapi)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.StdEncoding.EncodeToString(decoded)
	status, ok := statuses[publicKey]
	if !ok {
		t.Fatalf("peer %q was not parsed: %#v", publicKey, statuses)
	}
	if status.Endpoint != "[2001:db8::10]:443" ||
		status.LatestHandshake != 1785922000 ||
		status.LastRx != 1785922001 ||
		status.LastTx != 1785922002 ||
		status.TransferTx != 4567 ||
		status.TransferRx != 8910 {
		t.Fatalf("peer status = %#v", status)
	}
	if rendered := strings.Join([]string{
		status.PublicKey,
		status.Endpoint,
	}, "\n"); strings.Contains(rendered, privateKey) ||
		strings.Contains(rendered, presharedKey) {
		t.Fatal("runtime peer status exposed secret key material")
	}
}

func TestParsePeerStatusesRejectsInvalidCounters(t *testing.T) {
	uapi := "public_key=" + strings.Repeat("22", 32) + "\nrx_bytes=invalid\n"
	if _, err := parsePeerStatuses(uapi); err == nil ||
		!strings.Contains(err.Error(), "received bytes") {
		t.Fatalf("invalid counter error = %v", err)
	}
}
