package quic

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
)

func TestCarrierRoutesQUICThroughSalamander(t *testing.T) {
	key := obfs.Key{0x42}
	cfg := Config{
		HandshakeTimeout: time.Second,
		MaxIdleTimeout:   5 * time.Second,
		KeepAlivePeriod:  time.Second,
		ObfsMode:         "salamander",
		ObfsKeys:         []obfs.Key{key},
	}
	client, err := Open(0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := Open(0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for name, carrier := range map[string]*Carrier{"client": client, "server": server} {
		adapter, ok := carrier.transport.Conn.(*obfuscatedConn)
		if !ok || adapter.PacketConn != carrier.obfsConn {
			t.Fatalf("%s QUIC transport bypasses Salamander through %T", name, carrier.transport.Conn)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	accepted := make(chan *Connection, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, _, err := server.Accept(ctx)
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()
	remote := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), server.Port())
	outbound, err := client.Dial(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer outbound.CloseWithError("")
	var inbound *Connection
	select {
	case inbound = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer inbound.CloseWithError("")

	want := []byte("opaque WireGuard datagram")
	if err := outbound.SendDatagram(want); err != nil {
		t.Fatal(err)
	}
	got, err := inbound.ReceiveDatagram(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("received %q, want %q", got, want)
	}
}

func TestAddrPortRejectsNonUDPAddress(t *testing.T) {
	if _, err := addrPort(stringAddr("not UDP")); err == nil {
		t.Fatal("accepted a non-UDP remote address")
	}
}

type stringAddr string

func (a stringAddr) Network() string { return "test" }
func (a stringAddr) String() string  { return string(a) }

var _ net.Addr = stringAddr("")
