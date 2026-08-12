package armorbind

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
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

func TestBindRejectsHostnameResolution(t *testing.T) {
	bind := New(DefaultConfig())
	if _, err := bind.ParseEndpoint("localhost:443"); err == nil ||
		!strings.Contains(err.Error(), "numeric IP address") {
		t.Fatalf("hostname endpoint error = %v, want numeric-only rejection", err)
	}
}

func TestBindRejectsZeroEndpointPort(t *testing.T) {
	bind := New(DefaultConfig())
	if _, err := bind.ParseEndpoint("192.0.2.1:0"); err == nil ||
		!strings.Contains(err.Error(), "between 1 and 65535") {
		t.Fatalf("zero-port endpoint error = %v", err)
	}
}

func TestBindRoundTripAndClose(t *testing.T) {
	var debug bytes.Buffer
	configA := DefaultConfig()
	logger := log.New(&debug, "", 0).Printf
	configA.Debugf = logger
	configA.Eventf = logger
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
	if state := b.EndpointSessionState(source.(*Endpoint).addr); state != EndpointSessionEstablished {
		t.Fatalf("accepted endpoint session state = %q, want established", state)
	}
	if state := a.EndpointSessionState(aToB.(*Endpoint).addr); state != EndpointSessionEstablished {
		t.Fatalf("outbound endpoint session state = %q, want established", state)
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

	// If the accepted half of simultaneous dialing closes, WireGuard may
	// briefly retain its connection-scoped endpoint. Sending through that
	// stale identity must fall back to the maintained configured endpoint.
	acceptedAtA := sourceAtA.(*Endpoint)
	acceptedAtA.mu.Lock()
	acceptedSession := acceptedAtA.session
	acceptedAtA.mu.Unlock()
	acceptedSession.close()
	fallbackPayload := []byte("A reply through maintained fallback")
	if err := a.Send([][]byte{fallbackPayload}, sourceAtA); err != nil {
		t.Fatal(err)
	}
	gotB, _ = receiveOne(t, bReceive[0])
	if !bytes.Equal(gotB, fallbackPayload) {
		t.Fatalf("configured fallback payload changed: B=%q", gotB)
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
	restartedReceive, reboundPort := openRestartedBind(t, restarted, bPort)
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

func TestBindReconnectsAfterAbruptPeerRestartWithoutWireGuardTraffic(t *testing.T) {
	config := DefaultConfig()
	config.HandshakeTimeout = 300 * time.Millisecond
	config.MaxIdleTimeout = time.Second
	config.KeepAlivePeriod = 200 * time.Millisecond
	config.ReconnectMin = 50 * time.Millisecond
	config.ReconnectMax = 200 * time.Millisecond
	config.ReconnectJitter = func(value time.Duration) time.Duration { return value }
	a, b := New(config), New(config)
	restored := make(chan netip.AddrPort, 1)
	a.SetSessionRestored(func(endpoint netip.AddrPort) {
		restored <- endpoint
	})
	_, _, err := a.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	bReceive, bPort, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	aToB, err := a.ParseEndpoint(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(bPort))))
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Send([][]byte{[]byte("activate maintained endpoint")}, aToB); err != nil {
		t.Fatal(err)
	}
	receiveOne(t, bReceive[0])
	if got := a.EndpointReconnectStatus(aToB.(*Endpoint).addr).Attempts; got != 0 {
		t.Fatalf("automatic reconnect attempts before peer loss = %d, want 0", got)
	}
	select {
	case endpoint := <-restored:
		t.Fatalf("initial dial incorrectly reported a restored session for %s", endpoint)
	default:
	}

	b.mu.Lock()
	bState := b.state
	b.mu.Unlock()
	if err := bState.carrier.AbortNetwork(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := New(config)
	restartedReceive, reboundPort := openRestartedBind(t, restarted, bPort)
	defer restarted.Close()
	if reboundPort != bPort {
		t.Fatalf("restarted peer bound port %d, want %d", reboundPort, bPort)
	}
	waitForCondition(t, "automatic session recovery", func() bool {
		return a.EndpointReconnectStatus(aToB.(*Endpoint).addr).Attempts > 0 &&
			a.EndpointSessionState(aToB.(*Endpoint).addr) == EndpointSessionEstablished
	})
	select {
	case endpoint := <-restored:
		if endpoint != aToB.(*Endpoint).addr {
			t.Fatalf("restored endpoint = %s, want %s", endpoint, aToB.(*Endpoint).addr)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic reconnect did not notify the upper layer")
	}

	payload := []byte("first WireGuard packet after silent recovery")
	if err := a.Send([][]byte{payload}, aToB); err != nil {
		t.Fatal(err)
	}
	got, _ := receiveOne(t, restartedReceive[0])
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload after silent recovery = %q, want %q", got, payload)
	}
}

func TestSessionPathMigrationSnapshotsCurrentRemoteEndpoint(t *testing.T) {
	owner := New(DefaultConfig())
	configured := &Endpoint{
		owner:      owner,
		addr:       netip.MustParseAddrPort("192.0.2.10:51820"),
		configured: true,
	}
	accepted := &Endpoint{
		owner:    owner,
		addr:     netip.MustParseAddrPort("198.51.100.20:52821"),
		fallback: configured,
	}
	sess := &session{endpoint: accepted, remoteAddr: accepted.addr}
	accepted.session = sess

	migrated := sess.endpointForAddrPort(
		netip.MustParseAddrPort("198.51.100.21:52821"),
	)
	if migrated == accepted {
		t.Fatal("path migration reused the stale accepted endpoint")
	}
	if got := migrated.addr; got != netip.MustParseAddrPort("198.51.100.21:52821") {
		t.Fatalf("migrated endpoint = %s", got)
	}
	if migrated.session != sess {
		t.Fatal("migrated endpoint lost the established session")
	}
	if migrated.fallback != configured {
		t.Fatal("migrated endpoint lost its configured fallback")
	}
	if got := sess.endpointForAddrPort(accepted.addr); got != accepted {
		t.Fatal("unchanged QUIC path did not reuse its endpoint")
	}
}

func TestEndpointSessionStateFollowsCurrentRemotePath(t *testing.T) {
	bind := New(DefaultConfig())
	state := &runState{
		ctx:       context.Background(),
		sessions:  make(map[uint64]*session),
		endpoints: make(map[netip.AddrPort]*Endpoint),
	}
	accepted := &Endpoint{
		owner: bind,
		addr:  netip.MustParseAddrPort("198.51.100.20:52821"),
	}
	sess := &session{
		id:         1,
		state:      state,
		endpoint:   accepted,
		ctx:        context.Background(),
		conn:       new(quiccarrier.Connection),
		remoteAddr: netip.MustParseAddrPort("198.51.100.21:52821"),
	}
	state.sessions[sess.id] = sess
	accepted.session = sess
	bind.state = state

	if got := bind.EndpointSessionState(sess.remoteAddr); got != EndpointSessionEstablished {
		t.Fatalf("migrated endpoint session state = %q, want established", got)
	}
	if got := bind.EndpointSessionState(accepted.addr); got != EndpointSessionIdle {
		t.Fatalf("stale endpoint session state = %q, want idle", got)
	}
}

func TestBindRedialEndpointDoesNotWaitForWireGuardTraffic(t *testing.T) {
	config := DefaultConfig()
	config.HandshakeTimeout = 300 * time.Millisecond
	config.ReconnectJitter = func(value time.Duration) time.Duration { return value }
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
	if err := a.Send([][]byte{[]byte("activate endpoint")}, aToB); err != nil {
		t.Fatal(err)
	}
	receiveOne(t, bReceive[0])

	a.RedialEndpoint(aToB.(*Endpoint).addr)
	waitForCondition(t, "explicit endpoint redial", func() bool {
		return a.EndpointReconnectStatus(aToB.(*Endpoint).addr).Attempts == 1 &&
			a.EndpointSessionState(aToB.(*Endpoint).addr) == EndpointSessionEstablished
	})
	if got := a.EndpointReconnectStatus(aToB.(*Endpoint).addr).Attempts; got != 1 {
		t.Fatalf("automatic reconnect attempts after explicit redial = %d, want 1", got)
	}
}

func TestBindDoesNotDialAClosedConnectionScopedEndpoint(t *testing.T) {
	a, b := New(DefaultConfig()), New(DefaultConfig())
	_, _, err := a.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	bReceive, bPort, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	aToB, err := a.ParseEndpoint(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(bPort))))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send([][]byte{[]byte("establish accepted path")}, aToB); err != nil {
		t.Fatal(err)
	}
	_, source := receiveOne(t, bReceive[0])
	accepted := source.(*Endpoint)
	accepted.mu.Lock()
	acceptedSession := accepted.session
	accepted.mu.Unlock()
	acceptedSession.close()

	err = b.Send([][]byte{[]byte("must not reverse dial")}, accepted)
	if err == nil || !strings.Contains(err.Error(), "connection-scoped") {
		t.Fatalf("send on closed accepted endpoint returned %v, want connection-scoped rejection", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReconnectBackoffIsExponentiallyBounded(t *testing.T) {
	minimum := 50 * time.Millisecond
	maximum := 400 * time.Millisecond
	want := []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		400 * time.Millisecond,
	}
	for exponent, expected := range want {
		if got := reconnectBackoff(minimum, maximum, uint32(exponent)); got != expected {
			t.Fatalf("backoff exponent %d = %s, want %s", exponent, got, expected)
		}
	}
}

func TestReconnectFailureEventsAreExponentiallySampled(t *testing.T) {
	for attempt := uint64(1); attempt <= 16; attempt++ {
		want := attempt == 1 || attempt == 2 || attempt == 4 ||
			attempt == 8 || attempt == 16
		if got := reportReconnectAttempt(attempt); got != want {
			t.Fatalf("report reconnect attempt %d = %t, want %t", attempt, got, want)
		}
	}
}

func TestBindReportsScheduledReconnectBackoff(t *testing.T) {
	config := DefaultConfig()
	config.ReconnectMin = time.Hour
	config.ReconnectMax = time.Hour
	config.ReconnectJitter = func(value time.Duration) time.Duration { return value }
	bind := New(config)
	_, _, err := bind.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer bind.Close()
	parsed, err := bind.ParseEndpoint("192.0.2.20:443")
	if err != nil {
		t.Fatal(err)
	}
	ep := parsed.(*Endpoint)
	ep.mu.Lock()
	ep.activated = true
	ep.consecutiveFailures = 1
	ep.mu.Unlock()
	bind.mu.Lock()
	state := bind.state
	bind.mu.Unlock()

	bind.scheduleEndpointReconnect(state, ep, false)
	if got := bind.EndpointSessionState(ep.addr); got != EndpointSessionReconnecting {
		t.Fatalf("session state during backoff = %q, want reconnecting", got)
	}
	status := bind.EndpointReconnectStatus(ep.addr)
	if status.Attempts != 0 || status.Failures != 0 {
		t.Fatalf("scheduled reconnect status = %#v, want no completed attempts", status)
	}
	if status.NextReconnect <= time.Now().Unix() {
		t.Fatalf("next reconnect = %d, want a future timestamp", status.NextReconnect)
	}
}

func openRestartedBind(
	t *testing.T,
	bind *Bind,
	port uint16,
) ([]conn.ReceiveFunc, uint16) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		receive, reboundPort, err := bind.Open(port)
		if err == nil {
			return receive, reboundPort
		}
		if !errors.Is(err, syscall.EADDRINUSE) || time.Now().After(deadline) {
			t.Fatalf("rebind restarted peer to UDP port %d: %v", port, err)
		}
		// Other Go packages run concurrently and may briefly acquire the
		// just-released ephemeral port. Retry only that transient collision;
		// all other bind failures remain immediate test failures.
		time.Sleep(20 * time.Millisecond)
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

func waitForCondition(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
