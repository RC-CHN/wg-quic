//go:build windows

package platform

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsRawRouteAddressRoundTrip(t *testing.T) {
	for _, address := range []netip.Addr{
		netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("fe80::1%12"),
	} {
		raw, err := windowsRawAddress(address)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := windowsAddressFromRaw(raw)
		if err != nil {
			t.Fatal(err)
		}
		if roundTrip != address {
			t.Fatalf("route address round trip = %s, want %s", roundTrip, address)
		}
	}
}

func TestWindowsPeerRouteKeyPreservesCanonicalPrefix(t *testing.T) {
	for _, value := range []string{"10.20.0.9/16", "2001:db8:1::9/48"} {
		prefix := netip.MustParsePrefix(value)
		key, err := windowsPeerRouteKey(1, 77, prefix)
		if err != nil {
			t.Fatal(err)
		}
		if key.Destination != prefix.Masked().String() ||
			key.InterfaceLUID != 77 || key.CompartmentID != 1 {
			t.Fatalf("peer route key = %#v", key)
		}
		row, err := windowsRouteRow(key)
		if err != nil {
			t.Fatal(err)
		}
		if got := int(row.DestinationPrefix.PrefixLength); got != prefix.Bits() {
			t.Fatalf("route prefix length = %d, want %d", got, prefix.Bits())
		}
	}
}

func TestWindowsPeerRouteJournalRecoversProvenAndRetainsPendingAdd(t *testing.T) {
	root := t.TempDir()
	store := &windowsDiskRouteStore{
		stateDirectory:  filepath.Join(root, "state"),
		ownersDirectory: filepath.Join(root, "state", "owners"),
		ledgerPath:      filepath.Join(root, "state", windowsRouteLedgerFile),
		backupPath:      filepath.Join(root, "state", windowsRouteBackupFile),
		lockPath:        filepath.Join(root, "state", windowsRouteLockFile),
	}
	if err := store.prepare(); err != nil {
		t.Fatal(err)
	}
	before := netip.MustParsePrefix("10.1.0.0/16")
	after := netip.MustParsePrefix("10.2.0.0/16")
	beforeKey, _ := windowsPeerRouteKey(1, 77, before)
	afterKey, _ := windowsPeerRouteKey(1, 77, after)
	system := &fakeWindowsRouteSystem{
		selected: make(map[netip.Addr]windowsSelectedRoute),
		routes: map[windowsRouteKey]windowsSelectedRoute{
			beforeKey: {Key: beforeKey},
			afterKey:  {Key: afterKey},
		},
	}
	path := filepath.Join(store.stateDirectory, "peer-routes-test.json")
	journal := &windowsPeerRouteJournal{
		tunnel: "office", interfaceLUID: 77, compartmentID: 1,
		path: path, store: store, system: system,
		recovery: RecoveryStatus{State: "clean"},
	}
	if err := journal.Begin(t.Context(), "epoch:request", []netip.Prefix{before}, []netip.Prefix{after}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(t.Context(), peerRouteJournalAdding, true, false); err != nil {
		t.Fatal(err)
	}

	recovered := &windowsPeerRouteJournal{
		tunnel: "office", interfaceLUID: 77, compartmentID: 1,
		path: path, store: store, system: system,
		recovery: RecoveryStatus{State: "clean"},
	}
	if err := recovered.recover(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, exists := system.routes[beforeKey]; exists {
		t.Fatal("recovery retained a proven pre-transaction route")
	}
	if _, exists := system.routes[afterKey]; !exists {
		t.Fatal("recovery deleted an ambiguous pending-add route")
	}
	status := recovered.RecoveryStatus()
	if status.State != "degraded" || status.RetainedAmbiguousObjects != 1 {
		t.Fatalf("recovery status = %#v", status)
	}
}

func TestWindowsPeerRouteJournalRecoversEveryDurableTransactionPhase(t *testing.T) {
	tests := []struct {
		name             string
		phase            peerRouteJournalPhase
		removalsApplied  bool
		additionsApplied bool
		wantAfter        bool
		wantAmbiguous    int
	}{
		{name: "prepared", phase: peerRouteJournalPrepared, wantAfter: true, wantAmbiguous: 1},
		{name: "removing", phase: peerRouteJournalRemoving, wantAfter: true, wantAmbiguous: 1},
		{name: "removed", phase: peerRouteJournalRemoved, removalsApplied: true, wantAfter: true, wantAmbiguous: 1},
		{name: "adding", phase: peerRouteJournalAdding, removalsApplied: true, wantAfter: true, wantAmbiguous: 1},
		{name: "committed", phase: peerRouteJournalCommitted, removalsApplied: true, additionsApplied: true},
		{name: "rollback additions", phase: peerRouteJournalRollbackAdditions, removalsApplied: true, additionsApplied: true},
		{name: "rollback removals", phase: peerRouteJournalRollbackRemovals, removalsApplied: true, wantAfter: true, wantAmbiguous: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := testWindowsPeerRouteStore(t, root)
			before := netip.MustParsePrefix("10.11.0.0/16")
			after := netip.MustParsePrefix("10.12.0.0/16")
			beforeKey, _ := windowsPeerRouteKey(1, 77, before)
			afterKey, _ := windowsPeerRouteKey(1, 77, after)
			system := &fakeWindowsRouteSystem{
				selected: make(map[netip.Addr]windowsSelectedRoute),
				routes: map[windowsRouteKey]windowsSelectedRoute{
					beforeKey: {Key: beforeKey},
					afterKey:  {Key: afterKey},
				},
			}
			path := filepath.Join(store.stateDirectory, "peer-routes-phases.json")
			journal := testWindowsPeerRouteJournal(path, store, system)
			if err := journal.Begin(
				t.Context(), "old-epoch:request", []netip.Prefix{before}, []netip.Prefix{after},
			); err != nil {
				t.Fatal(err)
			}
			if test.phase != peerRouteJournalPrepared {
				if err := journal.Mark(
					t.Context(), test.phase, test.removalsApplied, test.additionsApplied,
				); err != nil {
					t.Fatal(err)
				}
			}

			recovered := testWindowsPeerRouteJournal(path, store, system)
			if err := recovered.recover(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if _, exists := system.routes[beforeKey]; exists {
				t.Fatal("recovery retained the proven pre-transaction route")
			}
			if _, exists := system.routes[afterKey]; exists != test.wantAfter {
				t.Fatalf("post-transaction route exists=%t, want %t", exists, test.wantAfter)
			}
			status := recovered.RecoveryStatus()
			if status.RetainedAmbiguousObjects != test.wantAmbiguous {
				t.Fatalf("recovery status = %#v", status)
			}
			wantState := "clean"
			if test.wantAmbiguous != 0 {
				wantState = "degraded"
			}
			if status.State != wantState {
				t.Fatalf("recovery state = %q, want %q", status.State, wantState)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("terminal recovery retained the active journal: %v", err)
			}
		})
	}
}

func TestWindowsPeerRouteJournalAdoptsCanonicalInterruptedAdd(t *testing.T) {
	root := t.TempDir()
	store := testWindowsPeerRouteStore(t, root)
	before := netip.MustParsePrefix("10.21.0.0/16")
	after := netip.MustParsePrefix("10.22.0.0/16")
	beforeKey, _ := windowsPeerRouteKey(1, 77, before)
	afterKey, _ := windowsPeerRouteKey(1, 77, after)
	system := &fakeWindowsRouteSystem{
		selected: make(map[netip.Addr]windowsSelectedRoute),
		routes: map[windowsRouteKey]windowsSelectedRoute{
			beforeKey: {Key: beforeKey},
			afterKey:  {Key: afterKey},
		},
	}
	path := filepath.Join(store.stateDirectory, "peer-routes-adopt.json")
	journal := testWindowsPeerRouteJournal(path, store, system)
	if err := journal.Begin(
		t.Context(), "old-epoch:request", []netip.Prefix{before}, []netip.Prefix{after},
	); err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(t.Context(), peerRouteJournalAdding, true, false); err != nil {
		t.Fatal(err)
	}

	recovered := testWindowsPeerRouteJournal(path, store, system)
	if err := recovered.recover(t.Context(), []netip.Prefix{after}); err != nil {
		t.Fatal(err)
	}
	if len(system.routes) != 0 {
		t.Fatalf("canonical adoption did not clear the old projection before reapply: %#v", system.routes)
	}
	if status := recovered.RecoveryStatus(); status.State != "clean" ||
		status.RetainedAmbiguousObjects != 0 {
		t.Fatalf("canonical adoption recovery status = %#v", status)
	}
}

func testWindowsPeerRouteStore(t *testing.T, root string) *windowsDiskRouteStore {
	t.Helper()
	store := &windowsDiskRouteStore{
		stateDirectory:  filepath.Join(root, "state"),
		ownersDirectory: filepath.Join(root, "state", "owners"),
		ledgerPath:      filepath.Join(root, "state", windowsRouteLedgerFile),
		backupPath:      filepath.Join(root, "state", windowsRouteBackupFile),
		lockPath:        filepath.Join(root, "state", windowsRouteLockFile),
	}
	if err := store.prepare(); err != nil {
		t.Fatal(err)
	}
	return store
}

func testWindowsPeerRouteJournal(
	path string,
	store *windowsDiskRouteStore,
	system windowsRouteSystem,
) *windowsPeerRouteJournal {
	return &windowsPeerRouteJournal{
		tunnel: "office", interfaceLUID: 77, compartmentID: 1,
		path: path, store: store, system: system,
		recovery: RecoveryStatus{State: "clean"},
	}
}

func TestWindowsPeerRouteJournalQuarantinesIdentityMismatch(t *testing.T) {
	root := t.TempDir()
	store := &windowsDiskRouteStore{
		stateDirectory:  filepath.Join(root, "state"),
		ownersDirectory: filepath.Join(root, "state", "owners"),
		ledgerPath:      filepath.Join(root, "state", windowsRouteLedgerFile),
		backupPath:      filepath.Join(root, "state", windowsRouteBackupFile),
		lockPath:        filepath.Join(root, "state", windowsRouteLockFile),
	}
	if err := store.prepare(); err != nil {
		t.Fatal(err)
	}
	prefix := netip.MustParsePrefix("10.3.0.0/16")
	oldKey, _ := windowsPeerRouteKey(1, 77, prefix)
	system := &fakeWindowsRouteSystem{
		selected: make(map[netip.Addr]windowsSelectedRoute),
		routes: map[windowsRouteKey]windowsSelectedRoute{
			oldKey: {Key: oldKey},
		},
	}
	path := filepath.Join(store.stateDirectory, "peer-routes-mismatch.json")
	journal := &windowsPeerRouteJournal{
		tunnel: "office", interfaceLUID: 77, compartmentID: 1,
		path: path, store: store, system: system,
		recovery: RecoveryStatus{State: "clean"},
	}
	if err := journal.Active(t.Context(), []netip.Prefix{prefix}); err != nil {
		t.Fatal(err)
	}

	recovered := &windowsPeerRouteJournal{
		tunnel: "office", interfaceLUID: 88, compartmentID: 1,
		path: path, store: store, system: system,
		recovery: RecoveryStatus{State: "clean"},
	}
	if err := recovered.recover(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active mismatch journal still exists: %v", err)
	}
	matches, err := filepath.Glob(path + ".identity-mismatch-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("identity-mismatch quarantine files = %#v, err = %v", matches, err)
	}
	if _, exists := system.routes[oldKey]; !exists {
		t.Fatal("identity mismatch recovery deleted an unproven old-LUID route")
	}
	status := recovered.RecoveryStatus()
	if status.State != "degraded" || status.RetainedAmbiguousObjects != 1 {
		t.Fatalf("recovery status = %#v", status)
	}
}

func TestWindowsRouteNotificationRowConversion(t *testing.T) {
	destination, err := windowsRawAddress(netip.MustParseAddr("192.0.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	nextHop, err := windowsRawAddress(netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	change, ok := windowsRouteChangeFromRow(&windows.MibIpForwardRow2{
		InterfaceLuid: 77,
		DestinationPrefix: windows.IpAddressPrefix{
			Prefix: destination, PrefixLength: 24,
		},
		NextHop: nextHop,
	})
	if !ok {
		t.Fatal("valid Windows route notification was rejected")
	}
	if change.Family != 4 || change.Destination != "192.0.2.0/24" ||
		change.InterfaceLUID != 77 || change.NextHop != "192.0.2.1" ||
		!change.Valid {
		t.Fatalf("route notification = %#v", change)
	}
}

func TestWindowsBestRouteCandidateUsesPrefixThenEffectiveMetric(t *testing.T) {
	current := windowsNativeRouteCandidate{
		route: windows.MibIpForwardRow2{
			InterfaceIndex: 10,
			DestinationPrefix: windows.IpAddressPrefix{
				PrefixLength: 0,
			},
			Metric: 5,
		},
		effectiveMetric: 20,
	}
	moreSpecific := current
	moreSpecific.route.InterfaceIndex = 20
	moreSpecific.route.DestinationPrefix.PrefixLength = 24
	moreSpecific.effectiveMetric = 100
	if !betterWindowsRouteCandidate(moreSpecific, current) {
		t.Fatal("longer route prefix did not win")
	}
	lowerMetric := current
	lowerMetric.route.InterfaceIndex = 30
	lowerMetric.effectiveMetric = 10
	if !betterWindowsRouteCandidate(lowerMetric, current) {
		t.Fatal("lower effective metric did not win equal-prefix comparison")
	}
}

func TestWindowsRouteStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := &windowsDiskRouteStore{
		stateDirectory:  filepath.Join(root, "state"),
		ownersDirectory: filepath.Join(root, "state", "owners"),
		ledgerPath:      filepath.Join(root, "state", windowsRouteLedgerFile),
		backupPath:      filepath.Join(root, "state", windowsRouteBackupFile),
		lockPath:        filepath.Join(root, "state", windowsRouteLockFile),
	}
	if err := store.prepare(); err != nil {
		t.Fatal(err)
	}
	owner, ownerLease, err := store.createOwnerLease("wg-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ownerLease.Close(); err != nil {
			t.Errorf("close owner lease: %v", err)
		}
	})

	unlock, err := store.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	selected := testWindowsSelectedRoute(netip.MustParseAddr("192.0.2.60"), 60, 4)
	ledger := windowsRouteLedger{
		SchemaVersion: windowsRouteLedgerSchemaVersion,
		Generation:    1,
		Routes: []windowsRouteRecord{{
			Key:            selected.Key,
			InterfaceIndex: selected.InterfaceIndex,
			Metric:         selected.Metric,
			BestSource:     selected.BestSource.String(),
			Ownership:      windowsRouteManaged,
			State:          windowsRouteActive,
			Owners:         []windowsRouteOwner{owner},
		}},
	}
	if err := store.Save(&ledger); err != nil {
		_ = unlock()
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if unlockErr := unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsRouteLedger(loaded); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Routes) != 1 || loaded.Routes[0].Key != selected.Key {
		t.Fatalf("loaded route ledger = %#v", loaded)
	}
}

func TestWindowsInterfaceAliasResolvesToLUID(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Name == "" {
			continue
		}
		luid, err := windowsInterfaceLUID(iface.Name)
		if err != nil {
			continue
		}
		if luid == 0 {
			t.Fatalf("interface %q resolved to an empty LUID", iface.Name)
		}
		if compartment := (windowsNativeRouteSystem{}).CurrentCompartmentID(); compartment == 0 {
			t.Fatal("current Windows network compartment is zero")
		}
		return
	}
	t.Fatal("no Windows network interface alias resolved to a LUID")
}
