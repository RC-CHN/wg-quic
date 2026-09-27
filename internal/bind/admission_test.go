package armorbind

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/conn"
)

func admissionFixture(t *testing.T) (*Bind, *runState) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.FECMode = "off"
	cfg.MaxSessions, cfg.MaxInboundSessions, cfg.MaxPendingInboundSessions = 3, 2, 1
	b := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &runState{ctx: ctx, cfg: b.cfg, sessions: make(map[uint64]*session), endpoints: make(map[netip.AddrPort]*Endpoint)}
	b.state = s
	return b, s
}

func inboundFixture(b *Bind, state *runState) *session {
	ep := &Endpoint{owner: b, addr: netip.MustParseAddrPort("192.0.2.1:443")}
	state.mu.Lock()
	s := b.newSessionLocked(state, ep, false)
	ep.session = s
	state.mu.Unlock()
	return s
}

func TestAdmissionBudgetsAndAuthenticationRelease(t *testing.T) {
	b, state := admissionFixture(t)
	first := inboundFixture(b, state)
	if state.admitSessionLocked(true) || !state.admitSessionLocked(false) {
		t.Fatal("pending inbound consumes reserved outbound capacity")
	}
	if !b.AssociateSessionPeer(first.id, "peer-a", 1) {
		t.Fatal("authentication failed")
	}
	if !state.admitSessionLocked(true) || state.pendingInbound != 0 {
		t.Fatal("authentication did not release pending budget")
	}
	second := inboundFixture(b, state)
	b.AssociateSessionPeer(second.id, "peer-b", 1)
	if state.admitSessionLocked(true) || !state.admitSessionLocked(false) {
		t.Fatal("inbound cap failed")
	}
	state.cfg.MaxSessions = 2
	if state.admitSessionLocked(false) {
		t.Fatal("global session cap failed")
	}
	first.expireAuthentication()
	if first.closed.Load() {
		t.Fatal("authenticated connection expired")
	}
	first.close()
	first.close()
	second.close()
	if state.inboundSessions != 0 || state.pendingInbound != 0 || len(state.sessions) != 0 {
		t.Fatal("closing leaked a budget reservation")
	}
}

func TestAuthenticationDeadlineRacesAreIdempotent(t *testing.T) {
	for range 100 {
		b, state := admissionFixture(t)
		s := inboundFixture(b, state)
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); s.expireAuthentication() }()
		go func() { defer wg.Done(); b.AssociateSessionPeer(s.id, "peer", 1) }()
		go func() { defer wg.Done(); s.close() }()
		wg.Wait()
		if state.pendingInbound != 0 || state.inboundSessions != 0 || b.stats.activeSessions.Load() != 0 {
			t.Fatal("admission reservation leaked or released twice")
		}
		if b.AssociateSessionPeer(s.id, "peer", 2) {
			t.Fatal("closed connection authenticated")
		}
	}
}

func TestUnauthenticatedReceiveBudgetIsReturnedOnShortBuffer(t *testing.T) {
	b, state := admissionFixture(t)
	s := inboundFixture(b, state)
	defer s.close()
	for range 32 {
		if !s.reserveUnauthenticatedReceive() {
			t.Fatal("early reservation rejection")
		}
	}
	if s.reserveUnauthenticatedReceive() {
		t.Fatal("unbounded unauthenticated ingress")
	}
	state.recv = make(chan receivedPacket, 1)
	state.recv <- receivedPacket{data: []byte("too long"), reservation: s}
	_, err := b.receiveFunc(state)([][]byte{{0}}, make([]int, 1), make([]conn.Endpoint, 1))
	if err == nil || s.pendingReceive.Load() != 31 {
		t.Fatal("short buffer leaked its queue reservation")
	}
}

func TestInboundAuthenticationTimerClosesIdleTransport(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InboundAuthenticationTimeout = 100 * time.Millisecond
	a, b := New(cfg), New(cfg)
	_, _, err := a.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, port, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ep, err := a.ParseEndpoint(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port).String())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send([][]byte{{1, 2, 3}}, ep); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.stats.authenticationTimeouts.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("untrusted inbound session did not expire")
}
