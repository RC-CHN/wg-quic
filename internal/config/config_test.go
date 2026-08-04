package config

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func testKey(v byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = v
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestParseCompatibleConfigAndUAPI(t *testing.T) {
	input := `[Interface]
PrivateKey = ` + testKey(1) + `
Address = 10.0.0.1/24, fd00::1/64
ListenPort = 443
FwMark = 0xca6c
DNS = 1.1.1.1
MTU = 1380
Table = auto
PreUp = echo pre
PostUp = echo post
PreDown = echo predown
PostDown = echo postdown
SaveConfig = false

# wg-quic: carrier=quic
# wg-quic: congestion=auto
# wg-quic: fec=off
# wg-quic: obfs=none

[Peer]
PublicKey = ` + testKey(2) + `
PresharedKey = ` + testKey(3) + `
AllowedIPs = 10.0.0.2/32, fd00::2/128
Endpoint = 127.0.0.1:8443
PersistentKeepalive = 25
# wg-quic: peer.fec-latency=balanced
`
	cfg, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Interface.ListenPort, uint16(443); got != want {
		t.Fatalf("ListenPort = %d, want %d", got, want)
	}
	if got, want := len(cfg.Interface.Addresses), 2; got != want {
		t.Fatalf("addresses = %d, want %d", got, want)
	}
	if got, want := cfg.Transport.FEC, "off"; got != want {
		t.Fatalf("FEC = %q, want %q", got, want)
	}
	if got, want := cfg.Peers[0].FECPolicy, "balanced"; got != want {
		t.Fatalf("peer FEC policy = %q, want %q", got, want)
	}
	uapi, err := cfg.UAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"listen_port=443\n", "protocol_version=1\n", "endpoint=127.0.0.1:8443\n", "allowed_ip=10.0.0.2/32\n"} {
		if !strings.Contains(uapi, want) {
			t.Errorf("UAPI does not contain %q:\n%s", want, uapi)
		}
	}
	if !strings.Contains(uapi, "fwmark=51820\n") {
		t.Fatalf("UAPI does not contain decimal fwmark:\n%s", uapi)
	}
	if strings.Contains(uapi, testKey(1)) {
		t.Fatal("UAPI must use WireGuard hex keys, not base64")
	}
}

func TestCloneDoesNotShareMutableConfigurationSlices(t *testing.T) {
	original := &Config{
		Interface: Interface{
			DNS:    []string{"1.1.1.1"},
			PreUp:  []string{"before"},
			PostUp: []string{"after"},
		},
		Peers: []Peer{{
			Endpoint:   "peer.example:443",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")},
		}},
	}
	clone := original.Clone()
	clone.Interface.DNS[0] = "9.9.9.9"
	clone.Interface.PreUp[0] = "changed"
	clone.Peers[0].Endpoint = "192.0.2.1:443"
	clone.Peers[0].AllowedIPs[0] = netip.MustParsePrefix("10.1.0.0/24")
	if original.Interface.DNS[0] != "1.1.1.1" ||
		original.Interface.PreUp[0] != "before" ||
		original.Peers[0].Endpoint != "peer.example:443" ||
		original.Peers[0].AllowedIPs[0] != netip.MustParsePrefix("10.0.0.0/24") {
		t.Fatalf("clone mutated original: %#v", original)
	}
}

func TestMinimalServerConfigAndKeepaliveOff(t *testing.T) {
	input := `[Interface]
PrivateKey = ` + testKey(1) + `
ListenPort = 51820

[Peer]
PublicKey = ` + testKey(2) + `
PersistentKeepalive = off
`
	cfg, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Interface.Addresses) != 0 || len(cfg.Peers[0].AllowedIPs) != 0 {
		t.Fatalf("unexpected inferred addresses: %#v", cfg)
	}
	if cfg.Transport.Obfs != "salamander" {
		t.Fatalf("default obfuscation = %q, want salamander", cfg.Transport.Obfs)
	}
}

func TestRejectsUnknownDirective(t *testing.T) {
	input := `[Interface]
PrivateKey = ` + testKey(1) + `
Address = 10.0.0.1/24
# wg-quic: fce=auto
[Peer]
PublicKey = ` + testKey(2) + `
AllowedIPs = 10.0.0.2/32
`
	_, err := Parse(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "unknown wg-quic directive") {
		t.Fatalf("got %v, want unknown directive error", err)
	}
}

func TestRejectsUnknownWireGuardKey(t *testing.T) {
	input := `[Interface]
PrivateKey = ` + testKey(1) + `
Address = 10.0.0.1/24
Adress = 10.0.0.3/24
[Peer]
PublicKey = ` + testKey(2) + `
AllowedIPs = 10.0.0.2/32
`
	_, err := Parse(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "unknown Interface key") {
		t.Fatalf("got %v, want unknown key error", err)
	}
}

func TestRejectsZeroEndpointPort(t *testing.T) {
	input := `[Interface]
PrivateKey = ` + testKey(1) + `
[Peer]
PublicKey = ` + testKey(2) + `
Endpoint = vpn.example.test:0
`
	_, err := Parse(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "port must be between 1 and 65535") {
		t.Fatalf("zero endpoint port error = %v", err)
	}
}

// The imported wireguard-go netns suite sends 130,560 AllowedIPs through UAPI
// to catch implementations that truncate large requests or responses. wg-quic
// intentionally exposes a file-oriented CLI instead of stock wg(8) UAPI, so
// exercise the equivalent boundary here: parse the complete wg-quick input and
// serialize every prefix into the forked Device IpcSet format.
func TestLargeAllowedIPsConfigurationIsNotTruncated(t *testing.T) {
	var input strings.Builder
	fmt.Fprintf(&input, "[Interface]\nPrivateKey = %s\n[Peer]\nPublicKey = %s\n", testKey(1), testKey(2))
	const want = 255 * 256 * 2
	for a := 1; a <= 255; a++ {
		for b := 0; b <= 255; b++ {
			fmt.Fprintf(&input, "AllowedIPs = %d.%d.0.0/16, %x::%x/128\n", a, b, a, b)
		}
	}
	cfg, err := Parse(strings.NewReader(input.String()))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.Peers[0].AllowedIPs); got != want {
		t.Fatalf("parsed AllowedIPs = %d, want %d", got, want)
	}
	uapi, err := cfg.UAPI()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(uapi, "allowed_ip="); got != want {
		t.Fatalf("serialized AllowedIPs = %d, want %d", got, want)
	}
}
