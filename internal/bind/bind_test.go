package armorbind

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"golang.zx2c4.com/wireguard/conn"
)

func TestBindRoundTripAndClose(t *testing.T) {
	a, b := New(DefaultConfig()), New(DefaultConfig())
	aReceive, _, err := a.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	bReceive, bPort, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	aToB, err := a.ParseEndpoint(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(bPort))))
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x5a}, 5000)
	if err := a.Send([][]byte{payload}, aToB); err != nil {
		t.Fatal(err)
	}
	got, source := receiveOne(t, bReceive[0])
	if !bytes.Equal(got, payload) {
		t.Fatal("payload changed in transit")
	}
	if err := b.Send([][]byte{payload}, source); err != nil {
		t.Fatal(err)
	}
	got, _ = receiveOne(t, aReceive[0])
	if !bytes.Equal(got, payload) {
		t.Fatal("reply changed in transit")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	packets := [][]byte{make([]byte, 65535)}
	sizes := make([]int, 1)
	eps := make([]conn.Endpoint, 1)
	if _, err := bReceive[0](packets, sizes, eps); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("receive after Close returned %v, want net.ErrClosed", err)
	}
}

func TestBindRoundTripWithKeyDerivedSalamander(t *testing.T) {
	config := DefaultConfig()
	config.ObfsMode = "salamander"
	config.ObfsKeys = []obfs.Key{{0x42}}
	a, b := New(config), New(config)
	aReceive, _, err := a.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	bReceive, bPort, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	aToB, err := a.ParseEndpoint(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(bPort))))
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0xa5}, 5000)
	if err := a.Send([][]byte{payload}, aToB); err != nil {
		t.Fatal(err)
	}
	got, source := receiveOne(t, bReceive[0])
	if !bytes.Equal(got, payload) {
		t.Fatal("obfuscated payload changed in transit")
	}
	if err := b.Send([][]byte{payload}, source); err != nil {
		t.Fatal(err)
	}
	got, _ = receiveOne(t, aReceive[0])
	if !bytes.Equal(got, payload) {
		t.Fatal("obfuscated reply changed in transit")
	}
}

func receiveOne(t *testing.T, receive conn.ReceiveFunc) ([]byte, conn.Endpoint) {
	t.Helper()
	type result struct {
		packet []byte
		ep     conn.Endpoint
		err    error
	}
	done := make(chan result, 1)
	go func() {
		packets := [][]byte{make([]byte, 65535)}
		sizes := make([]int, 1)
		eps := make([]conn.Endpoint, 1)
		n, err := receive(packets, sizes, eps)
		if err != nil || n != 1 {
			done <- result{err: err}
			return
		}
		done <- result{packet: packets[0][:sizes[0]], ep: eps[0]}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.packet, got.ep
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for datagram")
		return nil, nil
	}
}
