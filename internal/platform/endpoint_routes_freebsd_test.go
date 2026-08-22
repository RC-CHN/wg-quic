//go:build freebsd

package platform

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFreeBSDHostRouteCommandsUseExactAddressFamilyAndGateway(t *testing.T) {
	apply, undo := freeBSDHostRouteCommands(
		netip.MustParseAddr("192.0.2.10"),
		freeBSDGateway{address: "192.0.2.1"},
	)
	if !slices.Equal(apply.args, []string{
		"-q", "-n", "add", "-inet", "192.0.2.10", "-gateway", "192.0.2.1",
	}) {
		t.Fatalf("IPv4 endpoint route apply = %#v", apply.args)
	}
	if !slices.Equal(undo.args, []string{
		"-q", "-n", "delete", "-inet", "192.0.2.10",
	}) {
		t.Fatalf("IPv4 endpoint route undo = %#v", undo.args)
	}

	apply, undo = freeBSDHostRouteCommands(
		netip.MustParseAddr("2001:db8::10"),
		freeBSDGateway{interfaceName: "em0"},
	)
	if !slices.Equal(apply.args, []string{
		"-q", "-n", "add", "-inet6", "2001:db8::10", "-interface", "em0",
	}) {
		t.Fatalf("IPv6 endpoint route apply = %#v", apply.args)
	}
	if !slices.Equal(undo.args, []string{
		"-q", "-n", "delete", "-inet6", "2001:db8::10",
	}) {
		t.Fatalf("IPv6 endpoint route undo = %#v", undo.args)
	}
}

func TestFreeBSDRouteLeaseRetriesFailedRelease(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.10")
	deleteAttempts := 0
	manager := &freeBSDEndpointRouteLeaser{
		need4:   true,
		entries: make(map[netip.Addr]*freeBSDEndpointRouteEntry),
		defaultGateway: func(context.Context, bool) (freeBSDGateway, error) {
			return freeBSDGateway{address: "192.0.2.1"}, nil
		},
		runCommand: func(_ context.Context, _ string, args ...string) error {
			if slices.Contains(args, "delete") {
				deleteAttempts++
				if deleteAttempts == 1 {
					return errors.New("temporary route deletion failure")
				}
			}
			return nil
		},
	}
	lease, err := manager.AcquireEndpointRoute(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background()); err == nil {
		t.Fatal("first route release unexpectedly succeeded")
	}
	if entry := manager.entries[address]; entry == nil || entry.refs != 1 {
		t.Fatalf("failed release consumed route ownership: %#v", entry)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("retry route release: %v", err)
	}
	if _, ok := manager.entries[address]; ok {
		t.Fatal("successful retry retained route ownership")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	if deleteAttempts != 2 {
		t.Fatalf("route delete attempts = %d, want 2", deleteAttempts)
	}
}

func TestFreeBSDRouteLeaserCloseRetriesFailedCleanup(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.11")
	deleteAttempts := 0
	manager := &freeBSDEndpointRouteLeaser{
		need4:   true,
		entries: make(map[netip.Addr]*freeBSDEndpointRouteEntry),
		defaultGateway: func(context.Context, bool) (freeBSDGateway, error) {
			return freeBSDGateway{address: "192.0.2.1"}, nil
		},
		runCommand: func(_ context.Context, _ string, args ...string) error {
			if slices.Contains(args, "delete") {
				deleteAttempts++
				if deleteAttempts == 1 {
					return errors.New("temporary route deletion failure")
				}
			}
			return nil
		},
	}
	if _, err := manager.AcquireEndpointRoute(context.Background(), address); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err == nil {
		t.Fatal("first Close unexpectedly succeeded")
	}
	if manager.entries[address] == nil {
		t.Fatal("failed Close discarded the route entry")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if manager.entries[address] != nil || deleteAttempts != 2 {
		t.Fatalf("retry cleanup entry=%#v attempts=%d", manager.entries[address], deleteAttempts)
	}
}

func TestFreeBSDEndpointRouteRecoveryCoversEveryDurablePhase(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("production FreeBSD ledger recovery requires root-owned fixtures")
	}
	tests := []struct {
		name          string
		state         endpointRouteLedgerState
		wantDeleted   bool
		wantAmbiguous int
	}{
		{name: "pending add", state: endpointRoutePendingAdd, wantAmbiguous: 1},
		{name: "active", state: endpointRouteActive, wantDeleted: true},
		{name: "pending delete", state: endpointRoutePendingDelete, wantDeleted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			address := netip.MustParseAddr("192.0.2.90")
			gateway := freeBSDGateway{address: "192.0.2.1"}
			ledgerPath := filepath.Join(t.TempDir(), "endpoint-routes-v1.json")
			ledger := endpointRouteLedger{
				SchemaVersion: endpointRouteLedgerSchemaVersion,
				Records: []endpointRouteLedgerRecord{{
					Interface: "wg0", Address: address.String(), Gateway: gateway.address,
					State: test.state,
				}},
			}
			encoded, err := encodeEndpointRouteLedger(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFreeBSDEndpointRouteLedger(ledgerPath, encoded, false); err != nil {
				t.Fatal(err)
			}
			deleted := false
			manager := &freeBSDEndpointRouteLeaser{
				name: "wg0", ledgerPath: ledgerPath,
				entries: make(map[netip.Addr]*freeBSDEndpointRouteEntry),
				queryRoute: func(context.Context, netip.Addr) (freeBSDGateway, bool, error) {
					return gateway, true, nil
				},
				runCommand: func(_ context.Context, _ string, args ...string) error {
					if slices.Contains(args, "delete") {
						deleted = true
					}
					return nil
				},
			}
			if err := manager.recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if deleted != test.wantDeleted {
				t.Fatalf("matching route deleted=%t, want %t", deleted, test.wantDeleted)
			}
			status := manager.RecoveryStatus()
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
			if _, err := os.Stat(ledgerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovered ledger still exists: %v", err)
			}
		})
	}
}

func TestFreeBSDEndpointRouteRecoveryRetainsIdentityMismatch(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("production FreeBSD ledger recovery requires root-owned fixtures")
	}
	address := netip.MustParseAddr("192.0.2.91")
	ledgerPath := filepath.Join(t.TempDir(), "endpoint-routes-v1.json")
	ledger := endpointRouteLedger{
		SchemaVersion: endpointRouteLedgerSchemaVersion,
		Records: []endpointRouteLedgerRecord{{
			Interface: "wg0", Address: address.String(), Gateway: "192.0.2.1",
			State: endpointRouteActive,
		}},
	}
	encoded, err := encodeEndpointRouteLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFreeBSDEndpointRouteLedger(ledgerPath, encoded, false); err != nil {
		t.Fatal(err)
	}
	deleted := false
	manager := &freeBSDEndpointRouteLeaser{
		name: "wg0", ledgerPath: ledgerPath,
		entries: make(map[netip.Addr]*freeBSDEndpointRouteEntry),
		queryRoute: func(context.Context, netip.Addr) (freeBSDGateway, bool, error) {
			return freeBSDGateway{address: "192.0.2.254"}, true, nil
		},
		runCommand: func(context.Context, string, ...string) error {
			deleted = true
			return nil
		},
	}
	if err := manager.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("recovery deleted a route whose live gateway identity changed")
	}
}
