package armorbind

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/telemetry"
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

func TestRuntimeReceiveKeyLeasesAreReferenceCountedBeforeOpen(t *testing.T) {
	bind := New(Config{ObfsMode: "salamander"})
	key := obfs.Key{0x44}
	first := bind.AcquireReceiveKey(key)
	second := bind.AcquireReceiveKey(key)
	bind.mu.Lock()
	refs := bind.obfsReceive[key].refs
	bind.mu.Unlock()
	if refs != 2 {
		t.Fatalf("receive key refs = %d, want 2", refs)
	}
	first()
	bind.mu.Lock()
	refs = bind.obfsReceive[key].refs
	bind.mu.Unlock()
	if refs != 1 {
		t.Fatalf("receive key refs after first release = %d, want 1", refs)
	}
	second()
	bind.mu.Lock()
	_, exists := bind.obfsReceive[key]
	bind.mu.Unlock()
	if exists {
		t.Fatal("last receive key release retained registry entry")
	}
	second()
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

func TestBindDuplicatesPriorityDatagrams(t *testing.T) {
	a, b := New(DefaultConfig()), New(DefaultConfig())
	if _, _, err := a.Open(0); err != nil {
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

	// A WireGuard keepalive is a type-4 datagram with no encrypted payload:
	// 16-byte transport header plus the 16-byte AEAD tag. It must be sent
	// twice (once in each FEC group) so a burst cannot wipe out both copies.
	keepalive := make([]byte, 32)
	binary.LittleEndian.PutUint32(keepalive[:4], 4)
	if err := a.Send([][]byte{keepalive}, aToB); err != nil {
		t.Fatal(err)
	}

	first, _ := receiveOne(t, bReceive[0])
	second, _ := receiveOne(t, bReceive[0])
	if !bytes.Equal(first, keepalive) || !bytes.Equal(second, keepalive) {
		t.Fatalf("priority datagram was not duplicated: first=%x second=%x", first, second)
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

func TestSessionTelemetrySeparatesConnectionsAndAuthenticatedPeers(t *testing.T) {
	config := DefaultConfig()
	config.FECMode = "off"
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
	remote := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(bPort)))
	aToB, err := a.ParseEndpoint(remote)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("session-scoped telemetry")
	if err := a.Send([][]byte{payload}, aToB); err != nil {
		t.Fatal(err)
	}
	got, source := receiveOne(t, bReceive[0])
	if !bytes.Equal(got, payload) {
		t.Fatal("telemetry fixture payload changed in transit")
	}

	aSessions, omitted := a.SessionTelemetry()
	if omitted != 0 {
		t.Fatalf("outbound session telemetry omitted %d entries", omitted)
	}
	if len(aSessions) != 1 {
		t.Fatalf("outbound session telemetry = %#v", aSessions)
	}
	outbound := aSessions[0]
	if outbound.TelemetryVersion != 1 || outbound.Role != "outbound" ||
		outbound.State != "established" || outbound.SessionGeneration != 1 ||
		outbound.ConfiguredEndpoint != remote || outbound.CurrentEndpoint != remote ||
		outbound.EstablishedAt == nil || outbound.SampledAt.IsZero() {
		t.Fatalf("outbound session identity = %#v", outbound)
	}
	if outbound.Stats.WGTxPackets != 1 || outbound.Stats.WGTxBytes != uint64(len(payload)) ||
		outbound.Stats.WireTxPackets == 0 || outbound.Stats.QUICPacketsSent == 0 {
		t.Fatalf("outbound session counters = %#v", outbound.Stats)
	}
	if !a.AssociateSessionPeer(outbound.SessionID, "peer-z", 7) ||
		!a.AssociateSessionPeer(outbound.SessionID, "peer-a", 3) ||
		!a.AssociateSessionPeer(outbound.SessionID, "peer-z", 2) {
		t.Fatal("active session rejected authenticated peer association")
	}
	aSessions, _ = a.SessionTelemetry()
	outbound = aSessions[0]
	if len(outbound.Peers) != 2 || outbound.Peers[0].PublicKey != "peer-a" ||
		!outbound.Peers[0].Authenticated || outbound.Peers[0].EndpointGeneration != 3 ||
		outbound.Peers[1].PublicKey != "peer-z" ||
		outbound.Peers[1].EndpointGeneration != 7 {
		t.Fatalf("authenticated session peers = %#v", outbound.Peers)
	}

	bSessions, omitted := b.SessionTelemetry()
	if omitted != 0 {
		t.Fatalf("inbound session telemetry omitted %d entries", omitted)
	}
	if len(bSessions) != 1 {
		t.Fatalf("inbound session telemetry = %#v", bSessions)
	}
	inbound := bSessions[0]
	if inbound.SessionID != source.(*Endpoint).SessionID() || inbound.Role != "inbound" ||
		inbound.ConfiguredEndpoint != "" || inbound.CurrentEndpoint == "" ||
		inbound.Stats.WGRxPackets != 1 || inbound.Stats.WGRxBytes != uint64(len(payload)) ||
		inbound.Stats.WireRxPackets == 0 || inbound.Stats.QUICPacketsReceived == 0 {
		t.Fatalf("inbound session telemetry = %#v", inbound)
	}
}

func TestSessionTelemetryIsBoundedAndPrioritizesConfiguredSessions(t *testing.T) {
	bind := New(DefaultConfig())
	state := &runState{sessions: make(map[uint64]*session)}
	bind.state = state
	for id := uint64(1); id <= maxSessionTelemetry+2; id++ {
		role := "inbound"
		if id == maxSessionTelemetry+2 {
			role = "outbound"
		}
		state.sessions[id] = &session{
			id: id, generation: 1, role: role,
			authenticatedPeers: make(map[string]uint64),
		}
	}
	sessions, omitted := bind.SessionTelemetry()
	if len(sessions) != maxSessionTelemetry || omitted != 2 {
		t.Fatalf("bounded telemetry returned %d sessions and omitted %d", len(sessions), omitted)
	}
	if sessions[0].Role != "outbound" || sessions[0].SessionID != maxSessionTelemetry+2 {
		t.Fatalf("configured session was not prioritized: first = %#v", sessions[0])
	}
}

func TestRecentSessionTelemetryIsBoundedAndExpires(t *testing.T) {
	bind := New(DefaultConfig())
	now := time.Now()
	bind.recentSessions = []telemetry.ClosedSessionObservation{{
		FinalSequence: 1, SessionID: 1,
		ClosedAt: now.Add(-recentSessionTelemetryTTL),
	}}
	for id := uint64(2); id <= maxRecentSessionTelemetry+2; id++ {
		bind.retainClosedSession(telemetry.ClosedSessionObservation{
			SessionID: id, ClosedAt: now, State: "closed", Final: true,
		})
	}
	recent, evicted := bind.RecentSessionTelemetry()
	if len(recent) != maxRecentSessionTelemetry || evicted != 2 {
		t.Fatalf("recent sessions = %d, evicted = %d", len(recent), evicted)
	}
	if recent[0].SessionID != 3 || recent[len(recent)-1].SessionID != maxRecentSessionTelemetry+2 {
		t.Fatalf("bounded recent session order = %#v", recent)
	}
	for index := 1; index < len(recent); index++ {
		if recent[index].FinalSequence <= recent[index-1].FinalSequence {
			t.Fatalf("final sequences are not monotonic: %#v", recent)
		}
	}
}

func TestSessionEventsUseBoundedStreamCursor(t *testing.T) {
	bind := New(DefaultConfig())
	for id := uint64(1); id <= maxSessionEvents+3; id++ {
		bind.recordSessionEventAt(
			id, 1, telemetry.SessionEventPTO, "test", time.Now(), nil,
			&telemetry.SessionEventMetrics{PTOCount: id},
		)
	}
	batch, err := bind.SessionEvents("", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if batch.EventStreamID == "" || batch.FirstAvailableSequence != 4 ||
		batch.EventsDroppedTotal != 3 || len(batch.Events) != 2 ||
		batch.Events[0].EventSequence != 4 || batch.LastSequence != 5 {
		t.Fatalf("first event page = %#v", batch)
	}
	next, err := bind.SessionEvents(batch.EventStreamID, batch.LastSequence, 2)
	if err != nil || len(next.Events) != 2 || next.Events[0].EventSequence != 6 {
		t.Fatalf("second event page = %#v, error = %v", next, err)
	}
	if _, err := bind.SessionEvents("stale-stream", batch.LastSequence, 2); err == nil {
		t.Fatal("stale event stream cursor was accepted")
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
	unchanged := sess.endpointForAddrPort(accepted.addr)
	if unchanged == accepted || unchanged.addr != accepted.addr || unchanged.session != sess ||
		unchanged.fallback != configured {
		t.Fatalf("unchanged QUIC path snapshot = %#v", unchanged)
	}
	if migrated.ReceiveSequence() == 0 || unchanged.ReceiveSequence() <= migrated.ReceiveSequence() {
		t.Fatalf(
			"path snapshot sequences = migrated %d, unchanged %d",
			migrated.ReceiveSequence(), unchanged.ReceiveSequence(),
		)
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
	active, _ := a.SessionTelemetry()
	if len(active) != 1 {
		t.Fatalf("active sessions before redial = %#v", active)
	}
	oldSessionID := active[0].SessionID

	a.RedialEndpoint(aToB.(*Endpoint).addr)
	waitForCondition(t, "explicit endpoint redial", func() bool {
		return a.EndpointReconnectStatus(aToB.(*Endpoint).addr).Attempts == 1 &&
			a.EndpointSessionState(aToB.(*Endpoint).addr) == EndpointSessionEstablished
	})
	if got := a.EndpointReconnectStatus(aToB.(*Endpoint).addr).Attempts; got != 1 {
		t.Fatalf("automatic reconnect attempts after explicit redial = %d, want 1", got)
	}
	waitForCondition(t, "redial final session telemetry", func() bool {
		recent, _ := a.RecentSessionTelemetry()
		return len(recent) != 0 && recent[len(recent)-1].SessionID == oldSessionID &&
			recent[len(recent)-1].ReplacedBySessionID != 0
	})
	recent, evicted := a.RecentSessionTelemetry()
	final := recent[len(recent)-1]
	if evicted != 0 || final.CloseReason != telemetry.SessionCloseEndpointReplaced ||
		!final.Final || final.State != "closed" || final.FinalSequence == 0 ||
		final.ReplacedBySessionID == oldSessionID || final.FinalStats.WGTxPackets == 0 {
		t.Fatalf("redial final session telemetry = %#v, evicted = %d", final, evicted)
	}
	active, _ = a.SessionTelemetry()
	if len(active) != 1 || active[0].SessionID != final.ReplacedBySessionID ||
		active[0].ReplacesSessionID != oldSessionID || active[0].SessionGeneration <= final.SessionGeneration {
		t.Fatalf("replacement session telemetry = %#v, final = %#v", active, final)
	}
	events, err := a.SessionEvents("", 0, maxSessionEventQuery)
	if err != nil {
		t.Fatal(err)
	}
	var created, established, closed bool
	for _, event := range events.Events {
		if event.SessionID != oldSessionID {
			continue
		}
		switch event.EventType {
		case telemetry.SessionEventCreated:
			created = true
		case telemetry.SessionEventEstablished:
			established = true
		case telemetry.SessionEventClosed:
			closed = event.Reason == telemetry.SessionCloseEndpointReplaced && event.After != nil
		}
	}
	if !created || !established || !closed {
		t.Fatalf("redial lifecycle events = %#v", events)
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

func TestReceivedEndpointsCarryImmutableIngressSequence(t *testing.T) {
	bind := New(DefaultConfig())
	configured := &Endpoint{
		owner: bind, addr: netip.MustParseAddrPort("192.0.2.10:443"), configured: true,
	}
	session := &session{endpoint: configured}
	first := session.endpointForAddrPort(configured.addr)
	second := session.endpointForAddrPort(configured.addr)
	if first == configured || second == configured {
		t.Fatal("received packet reused mutable configured endpoint identity")
	}
	if first.ReceiveSequence() != 1 || second.ReceiveSequence() != 2 || bind.ReceiveSequence() != 2 {
		t.Fatalf(
			"receive sequences = %d, %d, bind %d",
			first.ReceiveSequence(), second.ReceiveSequence(), bind.ReceiveSequence(),
		)
	}
}
