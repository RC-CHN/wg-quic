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
)

type fakeWindowsRouteSystem struct {
	mu       sync.Mutex
	selected map[netip.Addr]windowsSelectedRoute
	routes   map[windowsRouteKey]windowsSelectedRoute
	created  []windowsRouteKey
	deleted  []windowsRouteKey
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
	}
}

func cloneWindowsRouteLedger(ledger windowsRouteLedger) windowsRouteLedger {
	clone := ledger
	clone.Routes = slices.Clone(ledger.Routes)
	for i := range clone.Routes {
		clone.Routes[i].Owners = slices.Clone(ledger.Routes[i].Owners)
	}
	return clone
}
