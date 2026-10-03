//go:build windows

package platform

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

func TestWindowsNetworkApplyPreservesConfirmedDNSOnProcessFailure(t *testing.T) {
	system := &fakeWindowsNetworkSystem{completed: []int{0}, applyErr: context.DeadlineExceeded}
	state := &windowsNetworkState{name: "wg0", interfaceLUID: 77, compartmentID: 9}
	operations := []windowsOperation{
		{mtu: 1280},
		{address: netip.MustParsePrefix("10.77.0.6/24")},
		{apply: "set DNS", undo: "reset DNS", dns: true},
	}
	// A process can report failure after it has emitted the DNS completion
	// record. Its successfully applied state still belongs to this cleanup.
	if err := state.apply(t.Context(), operations, system); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("apply=%v", err)
	}
	if err := state.rollback(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	want := []string{"configure:1280", "create-address:10.77.0.6/24", "apply", "dns", "address:10.77.0.6"}
	if !reflect.DeepEqual(system.calls, want) {
		t.Fatalf("lost confirmed DNS ownership: %v", system.calls)
	}
}

func TestWindowsNetworkApplyCancellationPrecedesMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	system := &fakeWindowsNetworkSystem{}
	state := &windowsNetworkState{name: "wg0", interfaceLUID: 77, compartmentID: 9}
	if err := state.apply(ctx, []windowsOperation{{mtu: 1280}}, system); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled apply=%v", err)
	}
	if len(system.calls) != 0 || len(state.undo) != 0 {
		t.Fatalf("mutated after cancellation: calls=%v undo=%v", system.calls, state.undo)
	}
}

func TestWindowsNativeStartupRejectsCancellationAndForeignCompartment(t *testing.T) {
	system := windowsNativeNetworkSystem{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := system.ConfigureInterface(ctx, 0, 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled interface change=%v", err)
	}
	if err := system.CreateAddress(ctx, 0, 0, netip.Prefix{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled address creation=%v", err)
	}
	compartment := system.CurrentCompartmentID()
	if err := system.ConfigureInterface(t.Context(), compartment+1, 77, 1280); err == nil {
		t.Fatal("configured another network compartment")
	}
	if err := system.CreateAddress(t.Context(), compartment+1, 77, netip.MustParsePrefix("192.0.2.77/32")); err == nil {
		t.Fatal("created address in another network compartment")
	}
	if err := system.ConfigureInterface(t.Context(), compartment, 0, 1280); err == nil {
		t.Fatal("configured unspecified interface")
	}
	if err := system.CreateAddress(t.Context(), compartment, 0, netip.MustParsePrefix("192.0.2.77/32")); err == nil {
		t.Fatal("created address on unspecified interface")
	}
}
