package peerendpoint

import (
	"net/netip"
	"testing"
)

func TestParseEndpointSpec(t *testing.T) {
	tests := []struct {
		input   string
		host    string
		port    uint16
		dynamic bool
	}{
		{"vpn.example.test:443", "vpn.example.test", 443, true},
		{"192.0.2.1:51820", "192.0.2.1", 51820, false},
		{"[2001:db8::1]:8443", "2001:db8::1", 8443, false},
		{"[::ffff:192.0.2.2]:53", "192.0.2.2", 53, false},
	}
	for _, test := range tests {
		spec, err := Parse(test.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.input, err)
		}
		if spec.Host != test.host || spec.Port != test.port || spec.Dynamic() != test.dynamic {
			t.Fatalf("Parse(%q) = %#v", test.input, spec)
		}
	}
}

func TestEndpointSpecRejectsInvalidHostAndPort(t *testing.T) {
	for _, input := range []string{
		"", ":443", "vpn.example.test:0", "vpn.example.test:65536",
		"vpn example.test:443", "2001:db8::1:443",
	} {
		if _, err := Parse(input); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", input)
		}
	}
}

func TestParseNumericEndpoint(t *testing.T) {
	got, err := ParseNumeric("[::ffff:192.0.2.2]:443")
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddrPort("192.0.2.2:443"); got != want {
		t.Fatalf("numeric endpoint = %s, want %s", got, want)
	}
	if _, err := ParseNumeric("vpn.example.test:443"); err == nil {
		t.Fatal("numeric parser accepted a hostname")
	}
}
