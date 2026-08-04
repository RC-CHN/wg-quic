package quick

import (
	"net/netip"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
)

func mustAddrPort(t *testing.T, value string) netip.AddrPort {
	t.Helper()
	endpoint, err := peerendpoint.ParseNumeric(value)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}
