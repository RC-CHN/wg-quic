package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestConfigurationSnapshotRoundTrip(t *testing.T) {
	privateKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	publicKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	cfg, err := Parse(strings.NewReader(fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.0.0.1/32
ListenPort = 443
DNS = 1.1.1.1, example.test
MTU = 1380
Table = auto

[Peer]
PublicKey = %s
AllowedIPs = 10.0.0.2/32
Endpoint = vpn.example.test:443
PersistentKeepalive = 25
`, privateKey, publicKey)))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := MarshalSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseSnapshot(bytes.NewReader(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, cfg) {
		t.Fatalf("snapshot round trip changed configuration:\n got: %#v\nwant: %#v", decoded, cfg)
	}
}

func TestConfigurationSnapshotRejectsUnknownVersionAndFields(t *testing.T) {
	for _, input := range []string{
		`{"version":2,"config":{}}`,
		`{"version":1,"config":{},"unexpected":true}`,
		`{"version":1,"config":{}} {}`,
	} {
		if _, err := ParseSnapshot(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid snapshot %q", input)
		}
	}
}
