package armorbind

import (
	"net/netip"
	"testing"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/conn"
)

var benchmarkReceiveEndpoint conn.Endpoint

func BenchmarkReceiveEndpointSnapshot(b *testing.B) {
	owner := New(DefaultConfig())
	s := &session{endpoint: &Endpoint{owner: owner}}
	remote := netip.MustParseAddrPort("192.0.2.1:443")
	b.ReportAllocs()
	for b.Loop() {
		benchmarkReceiveEndpoint = s.endpointForAddrPort(remote)
	}
}
