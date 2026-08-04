package armorbind

import (
	"bytes"
	"errors"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/conn"
)

func TestRuntimeEndpointKeyLeasesAreReferenceCounted(t *testing.T) {
	bind := New(DefaultConfig())
	endpoint := netip.MustParseAddrPort("192.0.2.20:443")
	key := obfs.Key{0x42}
	first, err := bind.AcquireEndpointKey(endpoint, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bind.AcquireEndpointKey(endpoint, key)
	if err != nil {
		t.Fatal(err)
	}
	if got := bind.obfsDynamic[endpoint].refs; got != 2 {
		t.Fatalf("dynamic endpoint refs = %d, want 2", got)
	}
	if _, err := bind.AcquireEndpointKey(endpoint, obfs.Key{0x43}); err == nil {
		t.Fatal("different key reused a leased endpoint")
	}
	first()
	first()
	if got := bind.obfsDynamic[endpoint].refs; got != 1 {
		t.Fatalf("dynamic endpoint refs after idempotent release = %d, want 1", got)
	}
	second()
	if _, ok := bind.obfsDynamic[endpoint]; ok {
		t.Fatal("last release retained dynamic endpoint lease")
	}
	if _, ok := bind.obfsResolved[endpoint]; ok {
		t.Fatal("last release retained outbound endpoint key")
	}
}

func TestBindRoundTripAndClose(t *testing.T) {
	var debug bytes.Buffer
	configA := DefaultConfig()
	configA.Debugf = log.New(&debug, "", 0).Printf
	a, b := New(configA), New(DefaultConfig())
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
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{
		"ArmorBind opened", "resolved peer endpoint", "dialing QUIC session",
		"QUIC session established", "ArmorBind closed",
	} {
		if !strings.Contains(debug.String(), message) {
			t.Errorf("debug log does not contain %q:\n%s", message, debug.String())
		}
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

func TestBindRejectsMismatchedSalamanderKeys(t *testing.T) {
	configA := DefaultConfig()
	configA.HandshakeTimeout = 250 * time.Millisecond
	configA.ObfsMode = "salamander"
	configA.ObfsKeys = []obfs.Key{{0x42}}
	configB := configA
	configB.ObfsKeys = []obfs.Key{{0x43}}
	a, b := New(configA), New(configB)
	_, _, err := a.Open(0)
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
	if err := a.Send([][]byte{[]byte("must remain unreadable")}, aToB); err != nil {
		t.Fatal(err)
	}

	received := make(chan error, 1)
	go func() {
		packets := [][]byte{make([]byte, 65535)}
		sizes := make([]int, 1)
		eps := make([]conn.Endpoint, 1)
		n, err := bReceive[0](packets, sizes, eps)
		if err == nil && n != 0 {
			err = errors.New("mismatched Salamander key delivered plaintext")
		}
		received <- err
	}()
	select {
	case err := <-received:
		if err == nil {
			t.Fatal("mismatched Salamander key unexpectedly completed QUIC")
		}
		t.Fatalf("receive returned before the negative-test window: %v", err)
	case <-time.After(750 * time.Millisecond):
	}
}

func TestBindSimultaneousDialKeepsAuthenticatedReplyPaths(t *testing.T) {
	a, b := New(DefaultConfig()), New(DefaultConfig())
	aReceive, aPort, err := a.Open(0)
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
	bToA, err := b.ParseEndpoint(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(aPort))))
	if err != nil {
		t.Fatal(err)
	}

	payloadA := []byte("simultaneous dial from A")
	payloadB := []byte("simultaneous dial from B")
	start := make(chan struct{})
	sendErrors := make(chan error, 2)
	go func() {
		<-start
		sendErrors <- a.Send([][]byte{payloadA}, aToB)
	}()
	go func() {
		<-start
		sendErrors <- b.Send([][]byte{payloadB}, bToA)
	}()
	close(start)
	for range 2 {
		if err := <-sendErrors; err != nil {
			t.Fatal(err)
		}
	}

	gotA, sourceAtA := receiveOne(t, aReceive[0])
	gotB, sourceAtB := receiveOne(t, bReceive[0])
	if !bytes.Equal(gotA, payloadB) || !bytes.Equal(gotB, payloadA) {
		t.Fatalf("simultaneous payloads changed: A=%q B=%q", gotA, gotB)
	}

	replyA := []byte("A reply on accepted connection")
	replyB := []byte("B reply on accepted connection")
	if err := a.Send([][]byte{replyA}, sourceAtA); err != nil {
		t.Fatal(err)
	}
	if err := b.Send([][]byte{replyB}, sourceAtB); err != nil {
		t.Fatal(err)
	}
	gotA, _ = receiveOne(t, aReceive[0])
	gotB, _ = receiveOne(t, bReceive[0])
	if !bytes.Equal(gotA, replyB) || !bytes.Equal(gotB, replyA) {
		t.Fatalf("connection-scoped replies changed: A=%q B=%q", gotA, gotB)
	}
}

func TestBindRedialsAfterAbruptPeerRestart(t *testing.T) {
	config := DefaultConfig()
	config.MaxIdleTimeout = time.Second
	config.KeepAlivePeriod = 250 * time.Millisecond
	a, b := New(config), New(config)
	_, _, err := a.Open(0)
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

	beforeRestart := []byte("before abrupt restart")
	if err := a.Send([][]byte{beforeRestart}, aToB); err != nil {
		t.Fatal(err)
	}
	got, _ := receiveOne(t, bReceive[0])
	if !bytes.Equal(got, beforeRestart) {
		t.Fatal("payload changed before restart")
	}

	b.mu.Lock()
	bState := b.state
	b.mu.Unlock()
	if bState == nil {
		t.Fatal("peer bind unexpectedly closed")
	}
	// Closing the packet socket first models process or host loss: the peer
	// cannot send a graceful QUIC CONNECTION_CLOSE to the surviving endpoint.
	if err := bState.carrier.AbortNetwork(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := New(config)
	restartedReceive, reboundPort, err := restarted.Open(bPort)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if reboundPort != bPort {
		t.Fatalf("restarted peer bound port %d, want %d", reboundPort, bPort)
	}

	afterRestart := []byte("after abrupt restart")
	stopSending := make(chan struct{})
	defer close(stopSending)
	sendErrors := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := a.Send([][]byte{afterRestart}, aToB); err != nil {
				select {
				case sendErrors <- err:
				default:
				}
			}
			select {
			case <-stopSending:
				return
			case <-ticker.C:
			}
		}
	}()
	got, _ = receiveOne(t, restartedReceive[0])
	select {
	case err := <-sendErrors:
		t.Fatalf("send while redialing failed: %v", err)
	default:
	}
	if !bytes.Equal(got, afterRestart) {
		t.Fatal("payload changed after restart")
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
