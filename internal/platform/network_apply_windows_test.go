//go:build windows

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
	"golang.org/x/sys/windows"
)

// This opt-in comparison uses fresh, owned Wintun devices and documentation
// addresses only. PowerShell is the existing behavioral reference, not part of
// the native timing measurement. Both devices stay alive for every snapshot.
func TestWindowsNetworkApplyNativeIntegration(t *testing.T) {
	if os.Getenv("WG_QUIC_TEST_WINDOWS_NETWORK") != "1" {
		t.Skip("requires an elevated isolated Windows guest with wintun.dll")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	system := windowsNativeNetworkSystem{}
	compartment := system.CurrentCompartmentID()
	type adapter struct {
		name string
		luid uint64
		cfg  *config.Config
	}
	create := func(label, ip4, ip6 string) adapter {
		t.Helper()
		cfg := &config.Config{Interface: config.Interface{MTU: 1420, Addresses: []netip.Prefix{netip.MustParsePrefix(ip4), netip.MustParsePrefix(ip6)}}, Peers: []config.Peer{{AllowedIPs: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8:88::/64")}}}}
		name := fmt.Sprintf("wgqapply-%s-%d", label, os.Getpid())
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
		return adapter{name, luid, cfg}
	}
	native := create("native", "192.0.2.221/32", "2001:db8:77::11/128")
	powershell := create("ps", "192.0.2.222/32", "2001:db8:77::12/128")
	snapshot := func(a adapter) map[string]any {
		t.Helper()
		index, err := windowsInterfaceIndexFromLUID(a.luid)
		if err != nil {
			t.Fatal(err)
		}
		wanted := []string{}
		for _, p := range a.cfg.Interface.Addresses {
			wanted = append(wanted, p.Addr().String())
		}
		// Exact address/destination filters omit adapter-specific link-local rows.
		script := fmt.Sprintf(`$ErrorActionPreference='Stop';Set-StrictMode -Version Latest;$ProgressPreference='SilentlyContinue';$i=%d;$wanted=%s;$routes=@('198.51.100.0/24','2001:db8:88::/64');
$interfaces=@(Get-NetIPInterface -InterfaceIndex $i -PolicyStore ActiveStore | Sort-Object AddressFamily | ForEach-Object {[pscustomobject]@{family=[int]$_.AddressFamily;dhcp=[string]$_.Dhcp;dad_transmits=[int]$_.DadTransmits;mtu=[int]$_.NlMtu;automatic_metric=[string]$_.AutomaticMetric;metric=[int]$_.InterfaceMetric;router_discovery=[string]$_.RouterDiscovery}});
$addresses=@(Get-NetIPAddress -InterfaceIndex $i -PolicyStore ActiveStore | Where-Object {$wanted -contains $_.IPAddress} | Sort-Object AddressFamily | ForEach-Object {[pscustomobject]@{family=[int]$_.AddressFamily;prefix_length=[int]$_.PrefixLength;prefix_origin=[string]$_.PrefixOrigin;suffix_origin=[string]$_.SuffixOrigin;skip_as_source=[bool]$_.SkipAsSource;dad_state=[string]$_.AddressState}});
$active_routes=@(Get-NetRoute -InterfaceIndex $i -PolicyStore ActiveStore | Where-Object {$routes -contains $_.DestinationPrefix} | Sort-Object DestinationPrefix | ForEach-Object {[pscustomobject]@{destination=[string]$_.DestinationPrefix;next_hop=[string]$_.NextHop;route_metric=[int]$_.RouteMetric;protocol=[string]$_.Protocol;publish=[string]$_.Publish}});
$persistent_addresses=@(Get-NetIPAddress -InterfaceIndex $i -PolicyStore PersistentStore -ErrorAction SilentlyContinue | Where-Object {$wanted -contains $_.IPAddress});
$persistent_routes=@(Get-NetRoute -InterfaceIndex $i -PolicyStore PersistentStore -ErrorAction SilentlyContinue | Where-Object {$routes -contains $_.DestinationPrefix});
[pscustomobject]@{interfaces=$interfaces;addresses=$addresses;routes=$active_routes;persistent_addresses=$persistent_addresses.Count;persistent_routes=$persistent_routes.Count}|ConvertTo-Json -Depth 6 -Compress`, index, powerShellArray(wanted))
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.WaitDelay = time.Second
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("snapshot %s: %v: %s", a.name, err, b)
		}
		var result map[string]any
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatalf("decode snapshot %s: %v: %s", a.name, err, b)
		}
		return result
	}
	log := func(label string, value map[string]any) { b, _ := json.Marshal(value); t.Logf("%s %s", label, b) }
	beforeNative, beforePS := snapshot(native), snapshot(powershell)
	log("native_before", beforeNative)
	log("powershell_before", beforePS)
	if !reflect.DeepEqual(beforeNative, beforePS) {
		t.Fatalf("fresh Wintun state differs before configuration")
	}
	start := time.Now()
	if err := system.ConfigureInterface(ctx, compartment, native.luid, uint32(native.cfg.EffectiveMTU())); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range native.cfg.Interface.Addresses {
		if err := system.CreateAddress(ctx, compartment, native.luid, prefix); err != nil {
			t.Fatal(err)
		}
		raw, err := windowsRawAddress(prefix.Addr())
		if err != nil {
			t.Fatal(err)
		}
		row := windows.MibUnicastIpAddressRow{
			Address: *(*windows.RawSockaddrInet6)(unsafe.Pointer(&raw)), InterfaceLuid: native.luid,
		}
		if err := windows.GetUnicastIpAddressEntry(&row); err != nil || row.DadState != 4 {
			t.Fatalf("native address %s is not immediately Preferred: DAD=%d err=%v", prefix, row.DadState, err)
		}
	}
	for _, prefix := range uniqueAllowedPrefixes(native.cfg) {
		key, err := windowsPeerRouteKey(compartment, native.luid, prefix)
		if err != nil {
			t.Fatal(err)
		}
		if err := system.CreateRoute(ctx, windowsSelectedRoute{Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("native apply duration %s", time.Since(start))
	ops, err := windowsNetworkOperations(powershell.name, powershell.cfg)
	if err != nil {
		t.Fatal(err)
	}
	scripts := make([]string, len(ops))
	for i, op := range ops {
		scripts[i] = op.apply
	}
	start = time.Now()
	completed, err := runWindowsNetworkBatch(ctx, powershell.name, scripts, false)
	if err != nil || len(completed) != len(ops) {
		t.Fatalf("PowerShell reference completed=%v err=%v", completed, err)
	}
	t.Logf("PowerShell reference apply duration %s", time.Since(start))
	afterNative, afterPS := snapshot(native), snapshot(powershell)
	log("native_after", afterNative)
	log("powershell_after", afterPS)
	// A mismatch is evidence to resolve, never permission to weaken parity.
	if !reflect.DeepEqual(afterNative, afterPS) {
		t.Errorf("native network state differs from existing PowerShell behavior")
	}
	if len(afterNative["addresses"].([]any)) != 2 || len(afterNative["routes"].([]any)) != 2 {
		t.Error("native state is missing expected dual-stack addresses or routes")
	}
	for _, raw := range afterNative["interfaces"].([]any) {
		v := raw.(map[string]any)
		if v["dad_transmits"] != float64(0) || v["mtu"] != float64(1420) {
			t.Errorf("native DAD/MTU mismatch: %v", v)
		}
	}
	for _, raw := range afterNative["addresses"].([]any) {
		v := raw.(map[string]any)
		if !strings.EqualFold(v["dad_state"].(string), "Preferred") {
			t.Errorf("native address is not immediately usable: %v", v)
		}
	}
}
