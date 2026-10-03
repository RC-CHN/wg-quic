//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
	"golang.org/x/sys/windows"
)

type fakeWindowsNetworkSystem struct {
	completed    []int
	applyErr     error
	dnsApplied   bool
	failures     map[string]error
	calls        []string
	luids        []uint64
	compartments []uint32
}

func (f *fakeWindowsNetworkSystem) RunBatch(ctx context.Context, _ string, luid uint64, scripts []string, keepGoing bool) ([]int, error) {
	f.luids = append(f.luids, luid)
	if !keepGoing {
		f.calls = append(f.calls, "apply")
		return f.completed, f.applyErr
	}
	f.calls = append(f.calls, "scripts:"+strings.Join(scripts, "|"))
	return nil, errors.Join(ctx.Err(), f.failures["scripts"])
}

func (f *fakeWindowsNetworkSystem) DeleteRoute(ctx context.Context, key windowsRouteKey) error {
	f.luids = append(f.luids, key.InterfaceLUID)
	f.compartments = append(f.compartments, key.CompartmentID)
	call := "route:" + key.Destination + ":" + key.NextHop
	f.calls = append(f.calls, call)
	return errors.Join(ctx.Err(), f.failures[call])
}

func (f *fakeWindowsNetworkSystem) DeleteAddress(ctx context.Context, compartment uint32, luid uint64, address netip.Addr) error {
	f.luids = append(f.luids, luid)
	f.compartments = append(f.compartments, compartment)
	call := "address:" + address.String()
	f.calls = append(f.calls, call)
	return errors.Join(ctx.Err(), f.failures[call])
}

func (f *fakeWindowsNetworkSystem) ResetDNS(ctx context.Context, compartment uint32, luid uint64) error {
	f.luids = append(f.luids, luid)
	f.compartments = append(f.compartments, compartment)
	f.calls = append(f.calls, "dns")
	return errors.Join(ctx.Err(), f.failures["dns"])
}

func (f *fakeWindowsNetworkSystem) ApplyDNS(ctx context.Context, compartment uint32, luid uint64, values []string) (bool, error) {
	err := f.applyNative(ctx, compartment, luid, "apply-dns")
	return f.dnsApplied || err == nil, err
}

func (f *fakeWindowsNetworkSystem) ConfigureInterface(ctx context.Context, compartment uint32, luid uint64, mtu uint32) error {
	return f.applyNative(ctx, compartment, luid, fmt.Sprintf("configure:%d", mtu))
}

func (f *fakeWindowsNetworkSystem) CreateAddress(ctx context.Context, compartment uint32, luid uint64, prefix netip.Prefix) error {
	return f.applyNative(ctx, compartment, luid, "create-address:"+prefix.String())
}

func (f *fakeWindowsNetworkSystem) CreateRoute(ctx context.Context, selected windowsSelectedRoute) error {
	return f.applyNative(ctx, selected.Key.CompartmentID, selected.Key.InterfaceLUID, "create-route:"+selected.Key.Destination+":"+selected.Key.NextHop)
}

func (f *fakeWindowsNetworkSystem) applyNative(ctx context.Context, compartment uint32, luid uint64, call string) error {
	f.luids = append(f.luids, luid)
	f.compartments = append(f.compartments, compartment)
	f.calls = append(f.calls, call)
	return errors.Join(ctx.Err(), f.failures[call])
}

func TestWindowsNetworkRollbackNativeDualStack(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{Addresses: []netip.Prefix{
			netip.MustParsePrefix("10.77.0.6/24"), netip.MustParsePrefix("fd77::6/64"),
		}},
		Peers: []config.Peer{{AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("10.88.0.0/16"), netip.MustParsePrefix("fd88::/64"),
		}}},
	}
	operations, err := windowsNetworkOperations("old-alias", cfg)
	if err != nil {
		t.Fatal(err)
	}
	system := &fakeWindowsNetworkSystem{}
	state := &windowsNetworkState{name: "old-alias", interfaceLUID: 77, compartmentID: 9}
	if err := state.apply(t.Context(), operations, system); err != nil {
		t.Fatal(err)
	}
	// Renaming or reusing the alias must never change the identity of an undo.
	state.name = "replacement-alias"
	if err := state.rollback(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"configure:1280", "create-address:10.77.0.6/24", "create-address:fd77::6/64",
		"create-route:fd88::/64:::", "create-route:10.88.0.0/16:0.0.0.0",
		"route:10.88.0.0/16:0.0.0.0", "route:fd88::/64:::", "address:fd77::6", "address:10.77.0.6",
	}
	if !reflect.DeepEqual(system.calls, want) {
		t.Fatalf("rollback calls = %v, want %v", system.calls, want)
	}
	for _, luid := range system.luids {
		if luid != 77 {
			t.Fatalf("lost captured LUID: %v", system.luids)
		}
	}
	for _, compartment := range system.compartments {
		if compartment != 9 {
			t.Fatalf("lost captured compartment: %v", system.compartments)
		}
	}
}

func TestWindowsNetworkRollbackOwnsOnlyCompletedApply(t *testing.T) {
	applyErr := errors.New("preexisting route rejected")
	operations := []windowsOperation{
		{apply: "mtu", mtu: 1280},
		{apply: "address", address: netip.MustParsePrefix("10.77.0.6/24")},
		{apply: "route", route: netip.MustParsePrefix("10.88.0.0/16")},
		{apply: "dns", undo: "restore DNS"},
	}
	system := &fakeWindowsNetworkSystem{failures: map[string]error{"create-route:10.88.0.0/16:0.0.0.0": applyErr}}
	state := &windowsNetworkState{name: "wg0", interfaceLUID: 77, compartmentID: 9}
	if err := state.apply(t.Context(), operations, system); !errors.Is(err, applyErr) {
		t.Fatalf("apply error: %v", err)
	}
	if err := state.rollback(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	if want := []string{"configure:1280", "create-address:10.77.0.6/24", "create-route:10.88.0.0/16:0.0.0.0", "address:10.77.0.6"}; !reflect.DeepEqual(system.calls, want) {
		t.Fatalf("undid an unowned or unattempted operation: %v", system.calls)
	}
}

func TestWindowsNetworkRollbackContinuesAfterDNSAndRouteFailures(t *testing.T) {
	dnsErr, routeErr := errors.New("DNS failure"), errors.New("route failure")
	system := &fakeWindowsNetworkSystem{failures: map[string]error{
		"dns": dnsErr, "route:10.88.0.0/16:0.0.0.0": routeErr,
	}}
	state := &windowsNetworkState{name: "wg0", interfaceLUID: 77, compartmentID: 9, undo: []windowsOperation{
		{address: netip.MustParsePrefix("10.77.0.6/24")},
		{route: netip.MustParsePrefix("10.88.0.0/16")},
		{undo: "restore DNS", dns: true},
	}}
	err := state.rollback(t.Context(), system)
	if !errors.Is(err, dnsErr) || !errors.Is(err, routeErr) || !strings.Contains(err.Error(), "10.88.0.0/16") {
		t.Fatalf("lost cleanup diagnostics: %v", err)
	}
	want := []string{"dns", "route:10.88.0.0/16:0.0.0.0", "address:10.77.0.6"}
	if !reflect.DeepEqual(system.calls, want) {
		t.Fatalf("cleanup stopped after failure: %v", system.calls)
	}
	for _, luid := range system.luids {
		if luid != 77 {
			t.Fatalf("cleanup lost original interface: %v", system.luids)
		}
	}
}

func TestWindowsNativeAddressDeleteRejectsCancellationAndWrongCompartment(t *testing.T) {
	system := windowsNativeRouteSystem{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := system.DeleteAddress(ctx, 0, 0, netip.Addr{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete after cancellation: %v", err)
	}
	compartment := system.CurrentCompartmentID()
	err := system.DeleteAddress(t.Context(), compartment+1, 77, netip.MustParseAddr("192.0.2.77"))
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(compartment+1)) {
		t.Fatalf("delete entered another compartment: %v", err)
	}
	if err := system.DeleteAddress(t.Context(), compartment, 0, netip.MustParseAddr("192.0.2.77")); err == nil {
		t.Fatal("delete accepted unspecified interface")
	}
}

func TestWindowsNetworkRollbackNativeIntegration(t *testing.T) {
	if os.Getenv("WG_QUIC_TEST_WINDOWS_NETWORK") != "1" {
		t.Skip("requires an elevated isolated Windows guest with wintun.dll")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cfg := &config.Config{
		Interface: config.Interface{Addresses: []netip.Prefix{
			netip.MustParsePrefix("192.0.2.212/32"), netip.MustParsePrefix("2001:db8:77::6/128"),
		}},
		Peers: []config.Peer{{AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8:88::/64"),
		}}},
	}
	system := windowsNativeRouteSystem{}
	compartment := system.CurrentCompartmentID()
	create := func(suffix string, cfg *config.Config) (uint64, Cleanup) {
		t.Helper()
		name := fmt.Sprintf("wgqtest-%s-%d", suffix, os.Getpid())
		device, err := tun.CreateTUN(name, cfg.EffectiveMTU())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := device.Close(); err != nil {
				t.Error(err)
			}
		})
		luid, err := windowsInterfaceLUID(name)
		if err != nil {
			t.Fatal(err)
		}
		cleanup, err := (windowsHost{}).ConfigureNetwork(ctx, name, cfg)
		if cleanup != nil {
			t.Cleanup(func() { _ = cleanup(context.Background()) })
		}
		if err != nil {
			t.Fatal(err)
		}
		return luid, cleanup
	}
	other := cfg.Clone()
	// Windows rejects the same local unicast address on two adapters, even
	// with DAD disabled. Route destinations may overlap and must stay scoped
	// to each LUID; each adapter also retains its own distinct addresses.
	other.Interface.Addresses = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.213/32"), netip.MustParsePrefix("2001:db8:77::7/128"),
	}
	first, cleanupFirst := create("a", cfg)
	second, cleanupSecond := create("b", other)
	assertObjects := func(luid uint64, cfg *config.Config, wantPresent bool) {
		t.Helper()
		for _, prefix := range cfg.Interface.Addresses {
			raw, err := windowsRawAddress(prefix.Addr())
			if err != nil {
				t.Fatal(err)
			}
			row := windows.MibUnicastIpAddressRow{
				Address: *(*windows.RawSockaddrInet6)(unsafe.Pointer(&raw)), InterfaceLuid: luid,
			}
			err = windows.GetUnicastIpAddressEntry(&row)
			if wantPresent && err != nil {
				t.Fatalf("LUID %d address %s missing: %v", luid, prefix, err)
			}
			if !wantPresent && !errors.Is(err, windows.ERROR_NOT_FOUND) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				t.Fatalf("LUID %d address %s still present or unreadable: %v", luid, prefix, err)
			}
		}
		for _, prefix := range cfg.Peers[0].AllowedIPs {
			key, err := windowsPeerRouteKey(compartment, luid, prefix)
			if err != nil {
				t.Fatal(err)
			}
			present, err := system.RouteExists(ctx, key)
			if err != nil || present != wantPresent {
				t.Fatalf("LUID %d route %s present=%v want=%v err=%v", luid, prefix, present, wantPresent, err)
			}
		}
	}
	assertObjects(first, cfg, true)
	assertObjects(second, other, true)
	start := time.Now()
	if err := cleanupFirst(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("dual-stack native cleanup completed in %s while both adapters remain open", time.Since(start))
	assertObjects(first, cfg, false)
	assertObjects(second, other, true)
	if err := cleanupFirst(ctx); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
	if err := cleanupSecond(ctx); err != nil {
		t.Fatal(err)
	}
	assertObjects(second, other, false)
}
