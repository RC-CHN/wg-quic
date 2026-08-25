package quic

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	quicgo "github.com/quic-go/quic-go"
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
		adapter, ok := carrier.transport.Conn.(*obfs.SalamanderConn)
		if !ok || adapter != carrier.obfsConn {
			t.Fatalf("%s QUIC transport bypasses Salamander through %T", name, carrier.transport.Conn)
		}
		overflow := carrier.ReceiveQueueOverflowStats()
		if runtime.GOOS == "linux" && (!overflow.Supported || overflow.Source != "linux_so_rxq_ovfl") {
			t.Fatalf("%s receive queue overflow stats = %#v", name, overflow)
		}
		if runtime.GOOS != "linux" && (overflow.Supported || overflow.Source != "unavailable") {
			t.Fatalf("%s receive queue overflow availability = %#v", name, overflow)
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

func TestClassifyConnectionErrorUsesTypedQUICErrors(t *testing.T) {
	tests := []struct {
		name, reason, class string
		err                 error
	}{
		{name: "idle", err: &quicgo.IdleTimeoutError{}, reason: "idle_timeout", class: "quic_idle_timeout"},
		{name: "handshake", err: context.DeadlineExceeded, reason: "handshake_timeout", class: "quic_handshake_timeout"},
		{name: "remote application", err: &quicgo.ApplicationError{Remote: true}, reason: "remote_close", class: "remote_application_close"},
		{name: "local transport", err: &quicgo.TransportError{}, reason: "transport_error", class: "quic_transport_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason, class, message := ClassifyConnectionError(test.err)
			if reason != test.reason || class != test.class || message == "" {
				t.Fatalf("classification = %q, %q, %q", reason, class, message)
			}
		})
	}
}

type stringAddr string

func (a stringAddr) Network() string { return "test" }
func (a stringAddr) String() string  { return string(a) }

var _ net.Addr = stringAddr("")
