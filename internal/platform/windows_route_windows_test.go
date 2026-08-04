//go:build windows

package platform

import (
	"net"
	"net/netip"
	"path/filepath"
	"testing"
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
