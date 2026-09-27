package armorbind

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

func endpointStatusFixture(count int) (*Bind, []netip.AddrPort) {
	b := New(DefaultConfig())
	state := &runState{ctx: context.Background(), sessions: make(map[uint64]*session), endpoints: make(map[netip.AddrPort]*Endpoint)}
	b.state = state
	addresses := make([]netip.AddrPort, count)
	for i := range addresses {
		addr := netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), uint16(i+1))
		addresses[i] = addr
		ep := &Endpoint{owner: b, addr: addr, reconnectAttempts: uint64(i), reconnectScheduled: true, nextReconnect: time.Unix(123, 0)}
		state.endpoints[addr] = ep
		sess := &session{id: uint64(i + 1), endpoint: ep, ctx: state.ctx}
		sess.remoteAddr.Store(&addr)
		if i%2 == 0 {
			sess.conn = new(quiccarrier.Connection)
		}
		if i%3 == 0 {
			sess.closed.Store(true)
		}
		state.sessions[sess.id] = sess
	}
	return b, addresses
}

func TestEndpointSnapshotMatchesQueriesAfterMigration(t *testing.T) {
	b, addresses := endpointStatusFixture(20)
	migrated := netip.MustParseAddrPort("[2001:db8::1]:443")
	b.state.sessions[2].remoteAddr.Store(&migrated)
	addresses = append(addresses, migrated, netip.MustParseAddrPort("203.0.113.1:1"))
	snapshot := b.EndpointStatusSnapshot()
	for _, addr := range addresses {
		got := snapshot.For(addr)
		if got.Session != b.EndpointSessionState(addr) || got.Reconnect != b.EndpointReconnectStatus(addr) {
			t.Fatalf("snapshot mismatch for %s: %+v", addr, got)
		}
	}
}

func BenchmarkEndpointStatus(b *testing.B) {
	for _, count := range []int{100, 1000} {
		bind, addresses := endpointStatusFixture(count)
		b.Run(fmt.Sprintf("peers-%d/repeated", count), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				for _, addr := range addresses {
					bind.EndpointSessionState(addr)
					bind.EndpointReconnectStatus(addr)
				}
			}
		})
		b.Run(fmt.Sprintf("peers-%d/indexed", count), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				observations := bind.EndpointStatusSnapshot()
				for _, addr := range addresses {
					observations.For(addr)
				}
			}
		})
	}
}
