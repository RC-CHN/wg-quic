package endpoint

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeResolver struct {
	mu        sync.Mutex
	responses map[string][]Resolution
	calls     []string
}

func (r *fakeResolver) Resolve(_ context.Context, host string) (Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, host)
	responses := r.responses[host]
	if len(responses) == 0 {
		return Resolution{}, errors.New("no fake DNS response")
	}
	response := responses[0]
	if len(responses) > 1 {
		r.responses[host] = responses[1:]
	}
	return response, nil
}

type fakeRouteLeaser struct {
	mu                sync.Mutex
	acquired          []netip.Addr
	leases            []*fakeRouteLease
	nextReleaseErrors []error
	changes           chan struct{}
	closed            bool
}

type fakeRouteLease struct {
	parent        *fakeRouteLeaser
	address       netip.Addr
	released      bool
	releaseCalls  int
	releaseErrors []error
	refreshCalls  int
	refreshResult bool
	refreshError  error
}

func (l *fakeRouteLeaser) AcquireEndpointRoute(_ context.Context, address netip.Addr) (RouteLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease := &fakeRouteLease{
		parent: l, address: address,
		releaseErrors: slices.Clone(l.nextReleaseErrors),
	}
	l.nextReleaseErrors = nil
	l.acquired = append(l.acquired, address)
	l.leases = append(l.leases, lease)
	return lease, nil
}

func (l *fakeRouteLeaser) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

func (l *fakeRouteLeaser) Changes() <-chan struct{} { return l.changes }

func (l *fakeRouteLease) Refresh(context.Context) (bool, error) {
	l.parent.mu.Lock()
	defer l.parent.mu.Unlock()
	l.refreshCalls++
	return l.refreshResult, l.refreshError
}

func (l *fakeRouteLease) Release(context.Context) error {
	l.parent.mu.Lock()
	defer l.parent.mu.Unlock()
	l.releaseCalls++
	if len(l.releaseErrors) != 0 {
		err := l.releaseErrors[0]
		l.releaseErrors = l.releaseErrors[1:]
		return err
	}
	l.released = true
	return nil
}

type fakeCoreControl struct {
	mu          sync.Mutex
	updates     []PeerUpdate
	waitError   map[netip.AddrPort]error
	activated   bool
	redialPeers []string
}

func (c *fakeCoreControl) SetPeerEndpoint(_ context.Context, update PeerUpdate) error {
	c.mu.Lock()
	c.updates = append(c.updates, update)
	c.mu.Unlock()
	return nil
}

func (c *fakeCoreControl) WaitPeerReady(_ context.Context, update PeerUpdate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitError[update.Endpoint]
}

func (c *fakeCoreControl) RedialPeer(_ context.Context, publicKey string) error {
	c.mu.Lock()
	c.redialPeers = append(c.redialPeers, publicKey)
	c.mu.Unlock()
	return nil
}

func (c *fakeCoreControl) Activate(context.Context) error {
	c.mu.Lock()
	c.activated = true
	c.mu.Unlock()
	return nil
}

func testSupervisor(
	t *testing.T,
	resolver *fakeResolver,
	routes *fakeRouteLeaser,
	core *fakeCoreControl,
	endpoint string,
) *Supervisor {
	t.Helper()
	supervisor, err := NewSupervisor(
		[]PeerSpec{{PublicKey: "peer", Endpoint: endpoint}},
		resolver,
		routes,
		core,
		Options{
			MinRefresh: time.Millisecond, MaxRefresh: time.Hour,
			ReadinessTimeout: time.Second,
			Jitter:           func(value time.Duration) time.Duration { return value },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return supervisor
}

func TestSupervisorInitializesOneCanonicalEndpointOwner(t *testing.T) {
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {{
			Addresses: []netip.Addr{
				netip.MustParseAddr("192.0.2.10"),
				netip.MustParseAddr("::ffff:192.0.2.10"),
			},
			RefreshAfter: time.Minute,
		}},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	selected, err := supervisor.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := netip.MustParseAddrPort("192.0.2.10:443")
	if selected["peer"] != want {
		t.Fatalf("selected endpoint = %s, want %s", selected["peer"], want)
	}
	if !slices.Equal(routes.acquired, []netip.Addr{want.Addr()}) {
		t.Fatalf("route acquisitions = %v", routes.acquired)
	}
	if len(core.updates) != 1 || core.updates[0].Endpoint != want || core.updates[0].Generation != 1 {
		t.Fatalf("core updates = %#v", core.updates)
	}
	if err := supervisor.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !core.activated {
		t.Fatal("prepared core was not activated")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.leases[0].released || !routes.closed {
		t.Fatal("supervisor did not release route ownership")
	}
}

func TestSupervisorRefreshesAfterDNSAddressRemoval(t *testing.T) {
	first := netip.MustParseAddr("192.0.2.10")
	second := netip.MustParseAddr("192.0.2.20")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{first}, RefreshAfter: time.Minute},
			{Addresses: []netip.Addr{second}, RefreshAfter: time.Minute},
		},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.RefreshPeer(context.Background(), "peer"); err != nil {
		t.Fatal(err)
	}
	want := netip.AddrPortFrom(second, 443)
	if got := supervisor.Selected()["peer"]; got != want {
		t.Fatalf("refreshed endpoint = %s, want %s", got, want)
	}
	if len(core.updates) != 2 || core.updates[1].Generation != 2 {
		t.Fatalf("core updates = %#v", core.updates)
	}
	if !routes.leases[0].released || routes.leases[1].released {
		t.Fatal("DNS switch did not transfer route ownership")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorKeepsCurrentAddressWhenDNSOrderChanges(t *testing.T) {
	first := netip.MustParseAddr("192.0.2.10")
	second := netip.MustParseAddr("192.0.2.20")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{first, second}, RefreshAfter: time.Minute},
			{Addresses: []netip.Addr{second, first}, RefreshAfter: time.Minute},
		},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.RefreshPeer(context.Background(), "peer"); err != nil {
		t.Fatal(err)
	}
	if len(core.updates) != 1 || len(routes.acquired) != 1 {
		t.Fatalf("DNS answer reordering caused a switch: updates=%v routes=%v", core.updates, routes.acquired)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRollsBackUnreadyDNSCandidate(t *testing.T) {
	first := netip.MustParseAddr("192.0.2.10")
	second := netip.MustParseAddr("192.0.2.20")
	secondEndpoint := netip.AddrPortFrom(second, 443)
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{first}, RefreshAfter: time.Minute},
			{Addresses: []netip.Addr{second}, RefreshAfter: time.Minute},
		},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{waitError: map[netip.AddrPort]error{
		secondEndpoint: errors.New("QUIC session did not establish"),
	}}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.RefreshPeer(context.Background(), "peer"); err == nil {
		t.Fatal("unready DNS candidate was accepted")
	}
	if got := supervisor.Selected()["peer"]; got != netip.AddrPortFrom(first, 443) {
		t.Fatalf("rollback endpoint = %s", got)
	}
	if len(core.updates) != 3 ||
		core.updates[1].Generation != 2 ||
		core.updates[2].Generation != 3 ||
		core.updates[2].Endpoint != netip.AddrPortFrom(first, 443) {
		t.Fatalf("rollback updates = %#v", core.updates)
	}
	if routes.leases[0].released || !routes.leases[1].released {
		t.Fatal("rollback released the wrong route lease")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRetriesFailedCandidateRouteReleaseOnClose(t *testing.T) {
	first := netip.MustParseAddr("192.0.2.10")
	second := netip.MustParseAddr("192.0.2.20")
	secondEndpoint := netip.AddrPortFrom(second, 443)
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{first}, RefreshAfter: time.Minute},
			{Addresses: []netip.Addr{second}, RefreshAfter: time.Minute},
		},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{waitError: map[netip.AddrPort]error{
		secondEndpoint: errors.New("QUIC session did not establish"),
	}}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	routes.mu.Lock()
	routes.nextReleaseErrors = []error{errors.New("temporary route delete failure")}
	routes.mu.Unlock()
	if err := supervisor.RefreshPeer(context.Background(), "peer"); err == nil {
		t.Fatal("failed candidate and route release unexpectedly succeeded")
	}
	if len(routes.leases) != 2 || routes.leases[1].releaseCalls != 1 ||
		routes.leases[1].released {
		t.Fatalf("candidate lease after failed release = %#v", routes.leases)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if routes.leases[1].releaseCalls != 2 || !routes.leases[1].released {
		t.Fatalf("Close did not retry candidate release: %#v", routes.leases[1])
	}
}

func TestSupervisorDoesNotResolveNumericEndpoint(t *testing.T) {
	resolver := &fakeResolver{responses: map[string][]Resolution{}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, "[2001:db8::10]:8443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("numeric endpoint invoked DNS resolver: %v", resolver.calls)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRedialsPeerAfterOuterRouteChanges(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.10")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{address}, RefreshAfter: time.Hour},
			{Addresses: []netip.Addr{address}, RefreshAfter: time.Hour},
		},
	}}
	routes := &fakeRouteLeaser{changes: make(chan struct{}, 1)}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	supervisor.options.NetworkDebounce = time.Millisecond
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	routes.leases[0].refreshResult = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	routes.changes <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for {
		core.mu.Lock()
		redialed := slices.Contains(core.redialPeers, "peer")
		core.mu.Unlock()
		resolver.mu.Lock()
		resolveCalls := len(resolver.calls)
		resolver.mu.Unlock()
		if redialed && resolveCalls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("network change did not redial the peer")
		}
		time.Sleep(time.Millisecond)
	}
	routes.mu.Lock()
	refreshCalls := routes.leases[0].refreshCalls
	routes.mu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("route refresh calls = %d, want 1", refreshCalls)
	}
	resolver.mu.Lock()
	resolveCalls := len(resolver.calls)
	resolver.mu.Unlock()
	if resolveCalls != 2 {
		t.Fatalf("DNS resolve calls = %d, want initialization plus network refresh", resolveCalls)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
