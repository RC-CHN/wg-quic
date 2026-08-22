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
	mu         sync.Mutex
	responses  map[string][]Resolution
	nextErrors []error
	calls      []string
}

func (r *fakeResolver) Resolve(_ context.Context, host string) (Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, host)
	if len(r.nextErrors) != 0 {
		err := r.nextErrors[0]
		r.nextErrors = r.nextErrors[1:]
		return Resolution{}, err
	}
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
	parent           *fakeRouteLeaser
	address          netip.Addr
	released         bool
	releaseCalls     int
	releaseErrors    []error
	refreshCalls     int
	refreshResult    bool
	refreshError     error
	refreshResponses []fakeRouteRefresh
}

type fakeRouteRefresh struct {
	changed bool
	err     error
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
	if len(l.refreshResponses) != 0 {
		response := l.refreshResponses[0]
		l.refreshResponses = l.refreshResponses[1:]
		return response.changed, response.err
	}
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
	mu           sync.Mutex
	updates      []PeerUpdate
	waitError    map[netip.AddrPort]error
	activated    bool
	redialPeers  []string
	redialErrors []error
	health       map[string]PeerHealth
	finalized    []PeerUpdate
}

func (c *fakeCoreControl) FinalizePeerEndpoint(
	_ context.Context,
	publicKey string,
	generation uint64,
) error {
	c.mu.Lock()
	c.finalized = append(c.finalized, PeerUpdate{PublicKey: publicKey, Generation: generation})
	c.mu.Unlock()
	return nil
}

func (c *fakeCoreControl) SetPeerEndpoint(_ context.Context, update PeerUpdate) error {
	c.mu.Lock()
	c.updates = append(c.updates, update)
	c.mu.Unlock()
	return nil
}

func (c *fakeCoreControl) ClearPeerEndpoint(
	_ context.Context,
	publicKey string,
	generation uint64,
) error {
	return c.SetPeerEndpoint(context.Background(), PeerUpdate{
		PublicKey: publicKey, Generation: generation,
	})
}

func (c *fakeCoreControl) WaitPeerReady(_ context.Context, update PeerUpdate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitError[update.Endpoint]
}

func (c *fakeCoreControl) RedialPeer(_ context.Context, publicKey string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.redialPeers = append(c.redialPeers, publicKey)
	if len(c.redialErrors) != 0 {
		err := c.redialErrors[0]
		c.redialErrors = c.redialErrors[1:]
		return err
	}
	return nil
}

func (c *fakeCoreControl) Activate(context.Context) error {
	c.mu.Lock()
	c.activated = true
	c.mu.Unlock()
	return nil
}

func (c *fakeCoreControl) PeerHealth(_ context.Context, publicKey string) (PeerHealth, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.health[publicKey], nil
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

func TestSupervisorRefreshAllUsesEachDynamicPeerTransaction(t *testing.T) {
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"one.example": {{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10")}, RefreshAfter: time.Minute}},
		"two.example": {{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.20")}, RefreshAfter: time.Minute}},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{waitError: map[netip.AddrPort]error{}}
	supervisor, err := NewSupervisor(
		[]PeerSpec{
			{PublicKey: "one", Endpoint: "one.example:443"},
			{PublicKey: "two", Endpoint: "two.example:443"},
			{PublicKey: "numeric", Endpoint: "192.0.2.30:443"},
		},
		resolver, routes, core,
		Options{MinRefresh: time.Millisecond, MaxRefresh: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	resolver.mu.Lock()
	resolver.calls = nil
	resolver.mu.Unlock()
	if err := supervisor.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	resolver.mu.Lock()
	calls := slices.Clone(resolver.calls)
	resolver.mu.Unlock()
	if !slices.Equal(calls, []string{"one.example", "two.example"}) {
		t.Fatalf("refresh calls = %#v", calls)
	}
}

func TestSupervisorStopContextBoundsWorkerWait(t *testing.T) {
	supervisor := &Supervisor{}
	supervisor.wg.Add(1)
	workerDone := make(chan struct{})
	go func() {
		<-workerDone
		supervisor.wg.Done()
	}()

	ctx, cancel := context.WithTimeout(
		context.Background(), 20*time.Millisecond,
	)
	defer cancel()
	err := supervisor.StopContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopContext error = %v, want deadline exceeded", err)
	}

	close(workerDone)
	retryCtx, retryCancel := context.WithTimeout(
		context.Background(), time.Second,
	)
	defer retryCancel()
	if err := supervisor.StopContext(retryCtx); err != nil {
		t.Fatalf("StopContext retry after worker exit: %v", err)
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

func TestSupervisorRetainsEstablishedEndpointWhenDNSRefreshFails(t *testing.T) {
	for name, failure := range map[string]error{
		"nxdomain": errors.New("lookup peer.example: no such host"),
		"timeout":  context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			address := netip.MustParseAddr("192.0.2.10")
			resolver := &fakeResolver{responses: map[string][]Resolution{
				"peer.example": {{
					Addresses: []netip.Addr{address}, RefreshAfter: time.Minute,
				}},
			}}
			routes := &fakeRouteLeaser{}
			core := &fakeCoreControl{}
			supervisor := testSupervisor(
				t, resolver, routes, core, "peer.example:443",
			)
			if _, err := supervisor.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			resolver.mu.Lock()
			resolver.nextErrors = []error{failure}
			resolver.mu.Unlock()
			if err := supervisor.RefreshPeer(context.Background(), "peer"); err == nil {
				t.Fatalf("%s refresh unexpectedly succeeded", name)
			}
			want := netip.AddrPortFrom(address, 443)
			if got := supervisor.Selected()["peer"]; got != want {
				t.Fatalf("selected endpoint after %s = %s, want %s", name, got, want)
			}
			if len(core.updates) != 1 || len(routes.leases) != 1 ||
				routes.leases[0].released {
				t.Fatalf(
					"%s refresh changed active ownership: updates=%#v leases=%#v",
					name, core.updates, routes.leases,
				)
			}
			if err := supervisor.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSupervisorHealthRotationSkipsStillPublishedAddress(t *testing.T) {
	first := netip.MustParseAddr("192.0.2.10")
	second := netip.MustParseAddr("192.0.2.20")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{first, second}, RefreshAfter: time.Hour},
			{Addresses: []netip.Addr{first, second}, RefreshAfter: time.Hour},
		},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{waitError: make(map[netip.AddrPort]error)}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.RotatePeer(context.Background(), "peer"); err != nil {
		t.Fatal(err)
	}
	if got := supervisor.Selected()["peer"]; got != netip.AddrPortFrom(second, 443) {
		t.Fatalf("health rotation selected %s", got)
	}
	if routes.leases[0].released != true || routes.leases[1].released {
		t.Fatal("health rotation did not transfer outer-route ownership")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorHealthWorkerRotatesAfterConsecutiveReconnectFailures(t *testing.T) {
	first := netip.MustParseAddr("192.0.2.30")
	second := netip.MustParseAddr("192.0.2.40")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{first, second}, RefreshAfter: time.Hour},
			{Addresses: []netip.Addr{first, second}, RefreshAfter: time.Hour},
		},
	}}
	routes := &fakeRouteLeaser{}
	core := &fakeCoreControl{
		waitError: make(map[netip.AddrPort]error),
		health: map[string]PeerHealth{
			"peer": {ConsecutiveReconnectFailures: 3},
		},
	}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	supervisor.options.HealthPoll = time.Millisecond
	supervisor.options.HealthFailureThreshold = 3
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	want := netip.AddrPortFrom(second, 443)
	waitForSupervisorCondition(t, "health-triggered endpoint rotation", func() bool {
		return supervisor.Selected()["peer"] == want
	})
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

func TestSupervisorCloseRetainsFailedActiveRouteLeaseForRetry(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.11")
	resolver := &fakeResolver{responses: map[string][]Resolution{}}
	routes := &fakeRouteLeaser{
		nextReleaseErrors: []error{errors.New("temporary route delete failure")},
	}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, address.String()+":443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := supervisor.Close(context.Background()); err == nil {
		t.Fatal("first Close unexpectedly consumed a failed route release")
	}
	if routes.leases[0].released || routes.closed {
		t.Fatal("failed Close released ownership or closed its route manager")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if routes.leases[0].releaseCalls != 2 || !routes.leases[0].released {
		t.Fatalf("active lease was not retried: %#v", routes.leases[0])
	}
	if !routes.closed {
		t.Fatal("successful Close did not close the route manager")
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
	deadline := time.Now().Add(5 * time.Second)
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

func TestSupervisorRetriesTransientRouteRefreshAfterNetworkChange(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.10")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {{Addresses: []netip.Addr{address}, RefreshAfter: time.Hour}},
	}}
	routes := &fakeRouteLeaser{changes: make(chan struct{}, 1)}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
	supervisor.options.NetworkDebounce = time.Millisecond
	supervisor.options.RetryMin = time.Millisecond
	supervisor.options.RetryMax = time.Millisecond
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	routes.leases[0].refreshResponses = []fakeRouteRefresh{
		{err: errors.New("new default route is not ready")},
		{changed: true},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	routes.changes <- struct{}{}
	waitForSupervisorCondition(t, "route refresh retry", func() bool {
		core.mu.Lock()
		defer core.mu.Unlock()
		return len(core.redialPeers) == 1
	})
	routes.mu.Lock()
	refreshCalls := routes.leases[0].refreshCalls
	routes.mu.Unlock()
	if refreshCalls != 2 {
		t.Fatalf("route refresh calls = %d, want initial failure plus retry", refreshCalls)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRetriesPendingRedialWithoutAnotherRouteChange(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.10")
	resolver := &fakeResolver{responses: map[string][]Resolution{}}
	routes := &fakeRouteLeaser{changes: make(chan struct{}, 1)}
	core := &fakeCoreControl{redialErrors: []error{
		errors.New("core is still activating"),
	}}
	supervisor := testSupervisor(t, resolver, routes, core, address.String()+":443")
	supervisor.options.NetworkDebounce = time.Millisecond
	supervisor.options.RetryMin = time.Millisecond
	supervisor.options.RetryMax = time.Millisecond
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	routes.leases[0].refreshResponses = []fakeRouteRefresh{{changed: true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	routes.changes <- struct{}{}
	waitForSupervisorCondition(t, "pending transport redial retry", func() bool {
		core.mu.Lock()
		defer core.mu.Unlock()
		return len(core.redialPeers) == 2
	})
	routes.mu.Lock()
	refreshCalls := routes.leases[0].refreshCalls
	routes.mu.Unlock()
	if refreshCalls != 2 {
		t.Fatalf("route refresh calls = %d, want retry after redial failure", refreshCalls)
	}
	supervisor.opMu.Lock()
	pending := supervisor.peers["peer"].routeRedialPending
	supervisor.opMu.Unlock()
	if pending {
		t.Fatal("successful retry retained pending route redial state")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorNewNetworkEventInterruptsRouteRetryBackoff(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.10")
	resolver := &fakeResolver{responses: map[string][]Resolution{}}
	routes := &fakeRouteLeaser{changes: make(chan struct{}, 1)}
	core := &fakeCoreControl{}
	supervisor := testSupervisor(t, resolver, routes, core, address.String()+":443")
	supervisor.options.NetworkDebounce = time.Millisecond
	supervisor.options.RetryMin = time.Hour
	supervisor.options.RetryMax = time.Hour
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	routes.leases[0].refreshResponses = []fakeRouteRefresh{
		{err: errors.New("default route disappeared")},
		{changed: true},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	routes.changes <- struct{}{}
	waitForSupervisorCondition(t, "first failed route refresh", func() bool {
		routes.mu.Lock()
		defer routes.mu.Unlock()
		return routes.leases[0].refreshCalls == 1
	})

	// The one-hour retry timer must be interrupted by the notification that
	// the replacement interface and default route are now ready.
	routes.changes <- struct{}{}
	waitForSupervisorCondition(t, "network event to interrupt route backoff", func() bool {
		core.mu.Lock()
		defer core.mu.Unlock()
		return len(core.redialPeers) == 1
	})
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func waitForSupervisorCondition(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}
