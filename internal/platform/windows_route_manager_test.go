package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeWindowsRouteSystem struct {
	mu        sync.Mutex
	selected  map[netip.Addr]windowsSelectedRoute
	routes    map[windowsRouteKey]windowsSelectedRoute
	created   []windowsRouteKey
	deleted   []windowsRouteKey
	bestErr   error
	bestCalls int
}

func (*fakeWindowsRouteSystem) CurrentCompartmentID() uint32 {
	return 1
}

func (s *fakeWindowsRouteSystem) BestRoute(
	_ context.Context,
	endpoint netip.Addr,
	_ uint64,
) (windowsSelectedRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bestCalls++
	if s.bestErr != nil {
		return windowsSelectedRoute{}, s.bestErr
	}
	selected, ok := s.selected[endpoint]
	if !ok {
		return windowsSelectedRoute{}, errors.New("no selected route")
	}
	return selected, nil
}

func (s *fakeWindowsRouteSystem) RouteExists(
	_ context.Context,
	key windowsRouteKey,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.routes[key]
	return ok, nil
}

func (s *fakeWindowsRouteSystem) CreateRoute(
	_ context.Context,
	selected windowsSelectedRoute,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.routes[selected.Key]; exists {
		return errors.New("route already exists")
	}
	s.routes[selected.Key] = selected
	s.created = append(s.created, selected.Key)
	return nil
}

func (s *fakeWindowsRouteSystem) DeleteRoute(
	_ context.Context,
	key windowsRouteKey,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.routes, key)
	s.deleted = append(s.deleted, key)
	return nil
}

type fakeWindowsRouteStore struct {
	mu     sync.Mutex
	ledger windowsRouteLedger
	alive  map[string]bool
}

type fakeWindowsRouteNotifier struct {
	mu      sync.Mutex
	ready   chan struct{}
	pending []windowsRouteChange
	closed  bool
}

func newFakeWindowsRouteNotifier() *fakeWindowsRouteNotifier {
	return &fakeWindowsRouteNotifier{ready: make(chan struct{}, 1)}
}

func (n *fakeWindowsRouteNotifier) Ready() <-chan struct{} { return n.ready }

func (n *fakeWindowsRouteNotifier) Drain() []windowsRouteChange {
	n.mu.Lock()
	defer n.mu.Unlock()
	changes := slices.Clone(n.pending)
	n.pending = nil
	return changes
}

func (n *fakeWindowsRouteNotifier) Close() error {
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	return nil
}

func (n *fakeWindowsRouteNotifier) emit(change windowsRouteChange) {
	n.mu.Lock()
	n.pending = append(n.pending, change)
	n.mu.Unlock()
	select {
	case n.ready <- struct{}{}:
	default:
	}
}

func (s *fakeWindowsRouteStore) Lock(context.Context) (func() error, error) {
	s.mu.Lock()
	return func() error {
		s.mu.Unlock()
		return nil
	}, nil
}

func (s *fakeWindowsRouteStore) Load() (windowsRouteLedger, error) {
	return cloneWindowsRouteLedger(s.ledger), nil
}

func (s *fakeWindowsRouteStore) Save(ledger *windowsRouteLedger) error {
	s.ledger = cloneWindowsRouteLedger(*ledger)
	return nil
}

func (s *fakeWindowsRouteStore) OwnerAlive(owner windowsRouteOwner) (bool, error) {
	return s.alive[owner.InstanceID], nil
}

func TestWindowsRouteManagerCreatesAndReleasesManagedRoute(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.10")
	selected := testWindowsSelectedRoute(endpoint, 10, 25)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")

	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(system.created) != 1 || system.created[0] != selected.Key {
		t.Fatalf("created routes = %#v", system.created)
	}
	if len(store.ledger.Routes) != 1 {
		t.Fatalf("ledger routes = %#v", store.ledger.Routes)
	}
	record := store.ledger.Routes[0]
	if record.Ownership != windowsRouteManaged ||
		record.State != windowsRouteActive ||
		record.Metric != 25 ||
		len(record.Owners) != 1 {
		t.Fatalf("managed record = %#v", record)
	}

	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("second release is not idempotent: %v", err)
	}
	if len(system.deleted) != 1 || system.deleted[0] != selected.Key {
		t.Fatalf("deleted routes = %#v", system.deleted)
	}
	if len(store.ledger.Routes) != 0 {
		t.Fatalf("ledger retained released route: %#v", store.ledger.Routes)
	}
}

func TestWindowsRouteManagerBorrowsExternalRouteWithoutDeletingIt(t *testing.T) {
	endpoint := netip.MustParseAddr("2001:db8::10")
	selected := testWindowsSelectedRoute(endpoint, 20, 7)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes:   map[windowsRouteKey]windowsSelectedRoute{selected.Key: selected},
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")

	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(system.created) != 0 {
		t.Fatalf("external route was recreated: %#v", system.created)
	}
	if got := store.ledger.Routes[0].Ownership; got != windowsRouteExternal {
		t.Fatalf("ownership = %q, want external", got)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(system.deleted) != 0 {
		t.Fatalf("external route was deleted: %#v", system.deleted)
	}
	if _, exists := system.routes[selected.Key]; !exists {
		t.Fatal("external route no longer exists")
	}
}

func TestWindowsRouteRepairLeavesLiveOwnerUntouched(t *testing.T) {
	selected := testWindowsSelectedRoute(
		netip.MustParseAddr("192.0.2.40"), 40, 4,
	)
	system := &fakeWindowsRouteSystem{
		routes: map[windowsRouteKey]windowsSelectedRoute{
			selected.Key: selected,
		},
	}
	store := &fakeWindowsRouteStore{
		alive: map[string]bool{"owner-live": true},
	}
	ledger := windowsRouteLedger{Routes: []windowsRouteRecord{{
		Key:       selected.Key,
		Ownership: windowsRouteManaged,
		State:     windowsRouteActive,
		Owners: []windowsRouteOwner{{
			Tunnel: "wg-live", InstanceID: "owner-live",
		}},
	}}}
	manager := &windowsRouteManager{system: system, store: store}

	changed, err := reconcileAbandonedWindowsRoutes(
		context.Background(), manager, &ledger,
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("repair changed a route with a live owner")
	}
	if len(system.deleted) != 0 || len(ledger.Routes) != 1 {
		t.Fatalf(
			"live route repair deleted=%v ledger=%#v",
			system.deleted, ledger.Routes,
		)
	}
}

func TestWindowsRouteRepairDeletesProvenAbandonedManagedRoute(t *testing.T) {
	selected := testWindowsSelectedRoute(
		netip.MustParseAddr("192.0.2.41"), 41, 4,
	)
	system := &fakeWindowsRouteSystem{
		routes: map[windowsRouteKey]windowsSelectedRoute{
			selected.Key: selected,
		},
	}
	store := &fakeWindowsRouteStore{alive: map[string]bool{}}
	ledger := windowsRouteLedger{Routes: []windowsRouteRecord{{
		Key:       selected.Key,
		Ownership: windowsRouteManaged,
		State:     windowsRouteActive,
		Owners: []windowsRouteOwner{{
			Tunnel: "wg-dead", InstanceID: "owner-dead",
		}},
	}}}
	manager := &windowsRouteManager{system: system, store: store}

	changed, err := reconcileAbandonedWindowsRoutes(
		context.Background(), manager, &ledger,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(ledger.Routes) != 0 {
		t.Fatalf("abandoned managed ledger = %#v, changed=%v", ledger.Routes, changed)
	}
	if !slices.Equal(system.deleted, []windowsRouteKey{selected.Key}) {
		t.Fatalf("deleted routes = %#v, want %#v", system.deleted, []windowsRouteKey{selected.Key})
	}
}

func TestWindowsRouteRepairDoesNotDeleteUnprovenKernelRoutes(t *testing.T) {
	for _, test := range []struct {
		name      string
		ownership windowsRouteOwnership
		state     windowsRouteState
	}{
		{
			name:      "external",
			ownership: windowsRouteExternal,
			state:     windowsRouteActive,
		},
		{
			name:      "ambiguous-pending-add",
			ownership: windowsRouteManaged,
			state:     windowsRoutePendingAdd,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := testWindowsSelectedRoute(
				netip.MustParseAddr("192.0.2.42"), 42, 4,
			)
			system := &fakeWindowsRouteSystem{
				routes: map[windowsRouteKey]windowsSelectedRoute{
					selected.Key: selected,
				},
			}
			store := &fakeWindowsRouteStore{alive: map[string]bool{}}
			ledger := windowsRouteLedger{Routes: []windowsRouteRecord{{
				Key:       selected.Key,
				Ownership: test.ownership,
				State:     test.state,
				Owners: []windowsRouteOwner{{
					Tunnel: "wg-dead", InstanceID: "owner-dead",
				}},
			}}}
			manager := &windowsRouteManager{system: system, store: store}

			changed, err := reconcileAbandonedWindowsRoutes(
				context.Background(), manager, &ledger,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !changed || len(ledger.Routes) != 0 {
				t.Fatalf("unproven ledger = %#v, changed=%v", ledger.Routes, changed)
			}
			if len(system.deleted) != 0 {
				t.Fatalf("repair deleted an unproven route: %#v", system.deleted)
			}
			if _, exists := system.routes[selected.Key]; !exists {
				t.Fatal("unproven kernel route no longer exists")
			}
		})
	}
}

func TestWindowsRouteManagerReferenceCountsMultipleOwners(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.20")
	selected := testWindowsSelectedRoute(endpoint, 30, 3)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	first := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	second := newFakeWindowsRouteManager(system, store, "wg1", "owner-2")

	firstLease, err := first.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	secondLease, err := second.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(system.created) != 1 {
		t.Fatalf("route created %d times", len(system.created))
	}
	if got := len(store.ledger.Routes[0].Owners); got != 2 {
		t.Fatalf("owner count = %d, want 2", got)
	}

	if err := firstLease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(system.deleted) != 0 {
		t.Fatal("shared route was deleted while one owner remained")
	}
	if err := secondLease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(system.deleted) != 1 {
		t.Fatalf("route delete count = %d, want 1", len(system.deleted))
	}
}

func TestWindowsRouteManagerReferenceCountsDuplicateEndpointLeases(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.21")
	selected := testWindowsSelectedRoute(endpoint, 31, 3)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")

	first, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.ledger.Routes[0].Owners[0].References; got != 2 {
		t.Fatalf("owner references = %d, want 2", got)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(system.deleted) != 0 {
		t.Fatal("route was deleted while a same-owner lease remained")
	}
	if err := second.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(system.deleted) != 1 {
		t.Fatalf("route delete count = %d, want 1", len(system.deleted))
	}
}

func TestWindowsRouteLeaseMigratesWithGetBestRouteResult(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.22")
	oldRoute := testWindowsSelectedRoute(endpoint, 32, 10)
	newRoute := testWindowsSelectedRoute(endpoint, 33, 5)
	newRoute.Key.NextHop = "198.51.100.1"
	newRoute.BestSource = netip.MustParseAddr("198.51.100.2")
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: oldRoute},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	system.selected[endpoint] = newRoute

	refreshable := lease.(interface {
		Refresh(context.Context) (bool, error)
	})
	changed, err := refreshable.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("new GetBestRoute2 result did not migrate the endpoint route")
	}
	if _, exists := system.routes[oldRoute.Key]; exists {
		t.Fatal("old endpoint route still exists")
	}
	if _, exists := system.routes[newRoute.Key]; !exists {
		t.Fatal("new endpoint route was not created")
	}
	record := store.ledger.Routes[0]
	if record.Key != newRoute.Key || record.Revision != 2 ||
		record.State != windowsRouteActive || record.Previous != nil {
		t.Fatalf("migrated route record = %#v", record)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(system.deleted, newRoute.Key) {
		t.Fatal("migrated lease did not release its current route")
	}
}

func TestWindowsRouteLeaseObservesMigrationByAnotherOwner(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.23")
	oldRoute := testWindowsSelectedRoute(endpoint, 34, 10)
	newRoute := testWindowsSelectedRoute(endpoint, 35, 5)
	newRoute.Key.NextHop = "198.51.100.1"
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: oldRoute},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	first := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	second := newFakeWindowsRouteManager(system, store, "wg1", "owner-2")
	firstLease, err := first.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	secondLease, err := second.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	system.selected[endpoint] = newRoute
	firstRefresh := firstLease.(interface {
		Refresh(context.Context) (bool, error)
	})
	if changed, err := firstRefresh.Refresh(context.Background()); err != nil || !changed {
		t.Fatalf("first refresh changed=%t err=%v", changed, err)
	}
	createdBefore := len(system.created)
	secondRefresh := secondLease.(interface {
		Refresh(context.Context) (bool, error)
	})
	if changed, err := secondRefresh.Refresh(context.Background()); err != nil || !changed {
		t.Fatalf("second refresh changed=%t err=%v", changed, err)
	}
	if len(system.created) != createdBefore {
		t.Fatal("second owner repeated an already committed route migration")
	}
}

func TestWindowsRouteLeaseRestoresOldRouteWhenSelectionFails(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.24")
	oldRoute := testWindowsSelectedRoute(endpoint, 36, 10)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: oldRoute},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	system.bestErr = errors.New("no usable route")
	refreshable := lease.(interface {
		Refresh(context.Context) (bool, error)
	})
	if changed, err := refreshable.Refresh(context.Background()); err == nil || changed {
		t.Fatalf("failed refresh changed=%t err=%v", changed, err)
	}
	if _, exists := system.routes[oldRoute.Key]; !exists {
		t.Fatal("failed route selection did not restore the old managed pin")
	}
	record := store.ledger.Routes[0]
	if record.Key != oldRoute.Key || record.State != windowsRouteActive ||
		record.Previous != nil {
		t.Fatalf("route ledger was not rolled back: %#v", record)
	}
}

func TestWindowsRouteLeaseReportsBestSourceChangeOnSamePath(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.26")
	selected := testWindowsSelectedRoute(endpoint, 38, 10)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	selected.BestSource = netip.MustParseAddr("192.0.2.99")
	system.selected[endpoint] = selected
	refreshable := lease.(interface {
		Refresh(context.Context) (bool, error)
	})
	changed, err := refreshable.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("best source change on the same interface did not request a redial")
	}
	if store.ledger.Routes[0].BestSource != "192.0.2.99" ||
		store.ledger.Routes[0].Revision != 2 {
		t.Fatalf("same-path source update = %#v", store.ledger.Routes[0])
	}
}

func TestWindowsRouteLeaseReevaluatesDisappearedManagedRoute(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.29")
	oldRoute := testWindowsSelectedRoute(endpoint, 42, 10)
	newRoute := testWindowsSelectedRoute(endpoint, 43, 5)
	newRoute.Key.NextHop = "198.51.100.1"
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: oldRoute},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}

	delete(system.routes, oldRoute.Key)
	system.selected[endpoint] = newRoute
	bestCallsBefore := system.bestCalls
	refreshable := lease.(interface {
		Refresh(context.Context) (bool, error)
	})
	changed, err := refreshable.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("disappeared route did not request a transport refresh")
	}
	if system.bestCalls == bestCallsBefore {
		t.Fatal("disappeared route was restored without consulting GetBestRoute2")
	}
	if _, exists := system.routes[oldRoute.Key]; exists {
		t.Fatal("stale route snapshot was recreated after route selection changed")
	}
	if _, exists := system.routes[newRoute.Key]; !exists {
		t.Fatal("newly selected route was not installed")
	}
	record := store.ledger.Routes[0]
	if record.Key != newRoute.Key || record.Revision != 2 {
		t.Fatalf("recovered route record = %#v", record)
	}
}

func TestWindowsRouteLeaseDoesNotRestoreDisappearedRouteWhenSelectionFails(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.33")
	oldRoute := testWindowsSelectedRoute(endpoint, 44, 10)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: oldRoute},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}

	delete(system.routes, oldRoute.Key)
	system.bestErr = errors.New("network unavailable")
	createdBefore := len(system.created)
	refreshable := lease.(interface {
		Refresh(context.Context) (bool, error)
	})
	if changed, err := refreshable.Refresh(context.Background()); err == nil || changed {
		t.Fatalf("failed refresh changed=%t err=%v", changed, err)
	}
	if len(system.created) != createdBefore {
		t.Fatal("failed route selection recreated a stale route snapshot")
	}
	if _, exists := system.routes[oldRoute.Key]; exists {
		t.Fatal("stale route exists after failed route selection")
	}
}

func TestWindowsRouteManagerRecoversInterruptedMigrations(t *testing.T) {
	t.Run("remove phase restores old managed route", func(t *testing.T) {
		endpoint := netip.MustParseAddr("192.0.2.27")
		oldRoute := testWindowsSelectedRoute(endpoint, 39, 10)
		system := &fakeWindowsRouteSystem{
			selected: map[netip.Addr]windowsSelectedRoute{endpoint: oldRoute},
			routes:   make(map[windowsRouteKey]windowsSelectedRoute),
		}
		store := newFakeWindowsRouteStore()
		manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
		previous := snapshotWindowsRoute(windowsRouteRecord{
			Key: oldRoute.Key, InterfaceIndex: oldRoute.InterfaceIndex,
			Metric: oldRoute.Metric, BestSource: oldRoute.BestSource.String(),
			Ownership: windowsRouteManaged, Revision: 1,
		})
		store.ledger.Routes = []windowsRouteRecord{{
			Key: oldRoute.Key, InterfaceIndex: oldRoute.InterfaceIndex,
			Metric: oldRoute.Metric, BestSource: oldRoute.BestSource.String(),
			Ownership: windowsRouteManaged, State: windowsRoutePendingMigrateRemove,
			Revision: 1, Previous: &previous,
			Owners: []windowsRouteOwner{manager.owner},
		}}
		if _, err := manager.AcquireEndpointRoute(context.Background(), endpoint); err != nil {
			t.Fatal(err)
		}
		if _, exists := system.routes[oldRoute.Key]; !exists {
			t.Fatal("interrupted remove phase did not restore the old route")
		}
		if record := store.ledger.Routes[0]; record.State != windowsRouteActive ||
			record.Previous != nil {
			t.Fatalf("recovered remove record = %#v", record)
		}
	})

	t.Run("add phase retains ambiguous existing route", func(t *testing.T) {
		endpoint := netip.MustParseAddr("192.0.2.28")
		oldRoute := testWindowsSelectedRoute(endpoint, 40, 10)
		newRoute := testWindowsSelectedRoute(endpoint, 41, 5)
		newRoute.Key.NextHop = "198.51.100.1"
		system := &fakeWindowsRouteSystem{
			selected: map[netip.Addr]windowsSelectedRoute{endpoint: newRoute},
			routes:   map[windowsRouteKey]windowsSelectedRoute{newRoute.Key: newRoute},
		}
		store := newFakeWindowsRouteStore()
		manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
		previous := snapshotWindowsRoute(windowsRouteRecord{
			Key: oldRoute.Key, InterfaceIndex: oldRoute.InterfaceIndex,
			Metric: oldRoute.Metric, BestSource: oldRoute.BestSource.String(),
			Ownership: windowsRouteManaged, Revision: 1,
		})
		store.ledger.Routes = []windowsRouteRecord{{
			Key: newRoute.Key, InterfaceIndex: newRoute.InterfaceIndex,
			Metric: newRoute.Metric, BestSource: newRoute.BestSource.String(),
			Ownership: windowsRouteManaged, State: windowsRoutePendingMigrateAdd,
			Revision: 1, Previous: &previous,
			Owners: []windowsRouteOwner{manager.owner},
		}}
		if _, err := manager.AcquireEndpointRoute(context.Background(), endpoint); err != nil {
			t.Fatal(err)
		}
		record := store.ledger.Routes[0]
		if record.State != windowsRouteActive ||
			record.Ownership != windowsRouteAmbiguous ||
			record.Previous != nil || record.Revision != 2 {
			t.Fatalf("recovered add record = %#v", record)
		}
	})
}

func TestWindowsRouteManagerIgnoresOwnedRouteNotifications(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.25")
	selected := testWindowsSelectedRoute(endpoint, 37, 10)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	lease, err := manager.AcquireEndpointRoute(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	notifier := newFakeWindowsRouteNotifier()
	manager.startRouteWatcher(notifier)
	notifier.emit(windowsRouteChange{
		Family:        selected.Key.Family,
		Destination:   selected.Key.Destination,
		InterfaceLUID: selected.Key.InterfaceLUID,
		NextHop:       selected.Key.NextHop,
		Valid:         true,
	})
	select {
	case <-manager.Changes():
		t.Fatal("owned route mutation escaped notification filtering")
	case <-time.After(20 * time.Millisecond):
	}

	notifier.emit(windowsRouteChange{
		Family:        4,
		Destination:   "0.0.0.0/0",
		InterfaceLUID: 99,
		NextHop:       "192.0.2.254",
		Valid:         true,
	})
	select {
	case <-manager.Changes():
	case <-time.After(time.Second):
		t.Fatal("external route change was not published")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsRouteManagerRecoversDeadOwnerAndKeepsAmbiguousPendingAdd(t *testing.T) {
	staleEndpoint := netip.MustParseAddr("192.0.2.30")
	stale := testWindowsSelectedRoute(staleEndpoint, 40, 1)
	ambiguousEndpoint := netip.MustParseAddr("192.0.2.31")
	ambiguous := testWindowsSelectedRoute(ambiguousEndpoint, 41, 1)
	newEndpoint := netip.MustParseAddr("192.0.2.32")
	selected := testWindowsSelectedRoute(newEndpoint, 42, 1)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{newEndpoint: selected},
		routes: map[windowsRouteKey]windowsSelectedRoute{
			stale.Key:     stale,
			ambiguous.Key: ambiguous,
		},
	}
	store := newFakeWindowsRouteStore()
	store.ledger.Routes = []windowsRouteRecord{
		{
			Key:       stale.Key,
			Ownership: windowsRouteManaged,
			State:     windowsRouteActive,
			Owners:    []windowsRouteOwner{testWindowsOwner("dead-1", "wg-old")},
		},
		{
			Key:       ambiguous.Key,
			Ownership: windowsRouteManaged,
			State:     windowsRoutePendingAdd,
			Owners:    []windowsRouteOwner{testWindowsOwner("dead-2", "wg-old")},
		},
	}
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")

	if _, err := manager.AcquireEndpointRoute(context.Background(), newEndpoint); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(system.deleted, stale.Key) {
		t.Fatalf("confirmed stale route was not deleted: %#v", system.deleted)
	}
	if slices.Contains(system.deleted, ambiguous.Key) {
		t.Fatal("ambiguous pending-add route was deleted")
	}
	if _, exists := system.routes[ambiguous.Key]; !exists {
		t.Fatal("ambiguous pending-add route did not remain in ActiveStore")
	}
	if routeRecordIndex(store.ledger.Routes, stale.Key) >= 0 ||
		routeRecordIndex(store.ledger.Routes, ambiguous.Key) >= 0 {
		t.Fatalf("dead owner records were retained: %#v", store.ledger.Routes)
	}
}

func TestWindowsRouteManagerRejectsRouteThroughTunnel(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.40")
	selected := testWindowsSelectedRoute(endpoint, 99, 0)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes:   make(map[windowsRouteKey]windowsSelectedRoute),
	}
	store := newFakeWindowsRouteStore()
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")
	manager.tunnelLUID = selected.Key.InterfaceLUID

	if _, err := manager.AcquireEndpointRoute(context.Background(), endpoint); err == nil {
		t.Fatal("route through the tunnel was accepted")
	}
	if len(system.created) != 0 || len(store.ledger.Routes) != 0 {
		t.Fatal("rejected route changed system or ledger state")
	}
}

func TestWindowsRouteManagerDoesNotMutateAnotherCompartment(t *testing.T) {
	foreignEndpoint := netip.MustParseAddr("192.0.2.45")
	foreign := testWindowsSelectedRoute(foreignEndpoint, 45, 0)
	foreign.Key.CompartmentID = 2
	endpoint := netip.MustParseAddr("192.0.2.46")
	selected := testWindowsSelectedRoute(endpoint, 46, 0)
	system := &fakeWindowsRouteSystem{
		selected: map[netip.Addr]windowsSelectedRoute{endpoint: selected},
		routes: map[windowsRouteKey]windowsSelectedRoute{
			foreign.Key: foreign,
		},
	}
	store := newFakeWindowsRouteStore()
	store.ledger.Routes = []windowsRouteRecord{{
		Key:       foreign.Key,
		Ownership: windowsRouteManaged,
		State:     windowsRouteActive,
		Owners:    []windowsRouteOwner{testWindowsOwner("dead-foreign", "wg-old")},
	}}
	manager := newFakeWindowsRouteManager(system, store, "wg0", "owner-1")

	if _, err := manager.AcquireEndpointRoute(context.Background(), endpoint); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(system.deleted, foreign.Key) {
		t.Fatal("route manager deleted a route from another compartment")
	}
	if routeRecordIndex(store.ledger.Routes, foreign.Key) < 0 {
		t.Fatal("route manager removed another compartment's ledger record")
	}
}

func TestWindowsRouteLedgerChecksumAndValidation(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.50")
	selected := testWindowsSelectedRoute(endpoint, 50, 9)
	ledger := windowsRouteLedger{
		SchemaVersion: windowsRouteLedgerSchemaVersion,
		Generation:    12,
		Routes: []windowsRouteRecord{{
			Key:            selected.Key,
			InterfaceIndex: selected.InterfaceIndex,
			Metric:         selected.Metric,
			BestSource:     selected.BestSource.String(),
			Ownership:      windowsRouteManaged,
			State:          windowsRouteActive,
			Owners:         []windowsRouteOwner{testWindowsOwner("owner-1", "wg0")},
		}},
	}
	encoded, err := encodeWindowsRouteLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWindowsRouteLedger(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Generation != ledger.Generation || len(decoded.Routes) != 1 {
		t.Fatalf("decoded ledger = %#v", decoded)
	}
	if err := validateWindowsRouteLedger(decoded); err != nil {
		t.Fatalf("valid ledger rejected: %v", err)
	}

	tampered := []byte(strings.Replace(
		string(encoded),
		`"routeMetric": 9`,
		`"routeMetric": 10`,
		1,
	))
	if _, err := decodeWindowsRouteLedger(tampered); err == nil {
		t.Fatal("ledger checksum accepted modified route state")
	}

	decoded.Routes = append(decoded.Routes, decoded.Routes[0])
	if err := validateWindowsRouteLedger(decoded); err == nil {
		t.Fatal("duplicate route key was accepted")
	}
}

func testWindowsSelectedRoute(
	endpoint netip.Addr,
	interfaceLUID uint64,
	metric uint32,
) windowsSelectedRoute {
	family := uint8(6)
	bits := 128
	nextHop := "2001:db8::1"
	source := netip.MustParseAddr("2001:db8::2")
	if endpoint.Is4() {
		family = 4
		bits = 32
		nextHop = "192.0.2.1"
		source = netip.MustParseAddr("192.0.2.2")
	}
	return windowsSelectedRoute{
		Key: windowsRouteKey{
			CompartmentID: 1,
			Family:        family,
			Destination:   netip.PrefixFrom(endpoint, bits).String(),
			InterfaceLUID: interfaceLUID,
			NextHop:       nextHop,
		},
		InterfaceIndex: uint32(interfaceLUID),
		Metric:         metric,
		BestSource:     source,
	}
}

func newFakeWindowsRouteStore() *fakeWindowsRouteStore {
	return &fakeWindowsRouteStore{
		ledger: windowsRouteLedger{
			SchemaVersion: windowsRouteLedgerSchemaVersion,
			Routes:        []windowsRouteRecord{},
		},
		alive: make(map[string]bool),
	}
}

func newFakeWindowsRouteManager(
	system windowsRouteSystem,
	store *fakeWindowsRouteStore,
	tunnel, ownerID string,
) *windowsRouteManager {
	owner := testWindowsOwner(ownerID, tunnel)
	store.alive[owner.InstanceID] = true
	return &windowsRouteManager{
		system: system,
		store:  store,
		owner:  owner,
	}
}

func testWindowsOwner(instanceID, tunnel string) windowsRouteOwner {
	sum := sha256.Sum256([]byte(instanceID))
	encoded := hex.EncodeToString(sum[:16])
	instanceID = encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] +
		"-" + encoded[16:20] + "-" + encoded[20:32]
	return windowsRouteOwner{
		Tunnel:     tunnel,
		InstanceID: instanceID,
		LeaseFile:  instanceID + ".lease",
		References: 1,
	}
}

func cloneWindowsRouteLedger(ledger windowsRouteLedger) windowsRouteLedger {
	clone := ledger
	clone.Routes = slices.Clone(ledger.Routes)
	for i := range clone.Routes {
		clone.Routes[i] = cloneWindowsRouteRecord(ledger.Routes[i])
	}
	return clone
}
