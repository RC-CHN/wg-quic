package quick

import (
	"net/netip"
	"testing"
)

func mustAddrPort(t *testing.T, value string) netip.AddrPort {
	t.Helper()
	endpoint, err := netip.ParseAddrPort(value)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}
