//go:build windows

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
	"golang.org/x/sys/windows"
)

func TestWindowsDNSRollbackFallsBackOnlyForMissingAPI(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want []string
	}{
		{"native", nil, []string{"dns"}},
		{"unavailable", fmt.Errorf("find: %w", errWindowsDNSAPIUnavailable), []string{"dns", "scripts:legacy DNS reset"}},
		{"denied", windows.ERROR_ACCESS_DENIED, []string{"dns"}},
		{"unsupported", windows.ERROR_NOT_SUPPORTED, []string{"dns"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			system := &fakeWindowsNetworkSystem{failures: map[string]error{"dns": test.err}}
			state := &windowsNetworkState{name: "wg0", interfaceLUID: 77, compartmentID: 9,
				undo: []windowsOperation{{dns: true, undo: "legacy DNS reset"}}}
			err := state.rollback(t.Context(), system)
			if test.name == "denied" || test.name == "unsupported" {
				if !errors.Is(err, test.err) {
					t.Fatalf("lost native failure: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(system.calls, test.want) {
				t.Fatalf("calls=%v want=%v", system.calls, test.want)
			}
			for _, luid := range system.luids {
				if luid != 77 {
					t.Fatalf("lost captured LUID: %v", system.luids)
				}
			}
		})
	}
}

func TestWindowsDNSResetTouchesOnlyServersAndConnectionSuffix(t *testing.T) {
	var flags []uint64
	err := resetWindowsDNSSettings(t.Context(), func(settings *windowsDNSInterfaceSettings) error {
		flags = append(flags, settings.Flags)
		if settings.Version != 1 || settings.NameServer == nil || *settings.NameServer != 0 {
			t.Fatalf("reset does not explicitly clear servers: %#v", settings)
		}
		if settings.Flags&windowsDNSSettingsIPv6 == 0 {
			if settings.Domain == nil || *settings.Domain != 0 {
				t.Fatal("connection suffix was not reset")
			}
		} else if settings.Domain != nil {
			t.Fatal("IPv6 reset unexpectedly writes the IPv4 suffix")
		}
		if settings.SearchList != nil || settings.ProfileNameServer != nil || settings.RegistrationEnabled != 0 || settings.RegisterAdapterName != 0 || settings.EnableLLMNR != 0 || settings.QueryAdapterName != 0 {
			t.Fatalf("unselected DNS settings populated: %#v", settings)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(flags, []uint64{0x22, 0x03}) {
		t.Fatalf("DNS reset flags: %v", flags)
	}
	if unsafe.Offsetof(windowsDNSInterfaceSettings{}.Flags) != 8 {
		t.Fatal("DNS settings ABI misaligned Flags")
	}
	pointerSize := unsafe.Sizeof(uintptr(0))
	if unsafe.Offsetof(windowsDNSInterfaceSettings{}.Domain) != 16 ||
		unsafe.Offsetof(windowsDNSInterfaceSettings{}.NameServer) != 16+pointerSize ||
		unsafe.Offsetof(windowsDNSInterfaceSettings{}.ProfileNameServer) != 32+3*pointerSize ||
		unsafe.Sizeof(windowsDNSInterfaceSettings{}) != 32+4*pointerSize {
		t.Fatal("DNS settings ABI has incorrect field offsets or size")
	}
}

func TestWindowsDNSResetContinuesAfterFailureAndHonorsCancellation(t *testing.T) {
	failure := windows.ERROR_ACCESS_DENIED
	calls := 0
	err := resetWindowsDNSSettings(t.Context(), func(*windowsDNSInterfaceSettings) error {
		calls++
		if calls == 1 {
			return failure
		}
		return nil
	})
	if calls != 2 || !errors.Is(err, failure) || !strings.Contains(err.Error(), "IPv4") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	calls = 0
	err = resetWindowsDNSSettings(ctx, func(*windowsDNSInterfaceSettings) error { calls++; cancel(); return nil })
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation calls=%d err=%v", calls, err)
	}
	err = (windowsNativeNetworkSystem{}).ResetDNS(ctx, 0, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled native call: %v", err)
	}
}

func TestWindowsDNSResetNativeIntegration(t *testing.T) {
	if os.Getenv("WG_QUIC_TEST_WINDOWS_NETWORK") != "1" {
		t.Skip("requires an elevated isolated Windows guest with wintun.dll")
	}
	if err := windowsProcSetDNSSettings.Find(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	system := windowsNativeNetworkSystem{}
	compartment := system.CurrentCompartmentID()
	type adapter struct {
		name string
		luid uint64
		guid windows.GUID
	}
	create := func(suffix string) adapter {
		t.Helper()
		name := fmt.Sprintf("wgqdns-%s-%d", suffix, os.Getpid())
		device, err := tun.CreateTUN(name, 1280)
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
		guid, err := windowsInterfaceGUID(luid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := system.ResetDNS(context.Background(), compartment, luid); err != nil {
				t.Error(err)
			}
		})
		return adapter{name, luid, guid}
	}
	first, second := create("a"), create("b")
	setSearchList := func(t *testing.T, a adapter, value string) {
		t.Helper()
		// The existing DNS apply/reset never owns SearchList. Leave one set
		// throughout the comparison to detect accidental unrelated changes.
		search, err := windows.UTF16PtrFromString(value)
		if err != nil {
			t.Fatal(err)
		}
		settings := windowsDNSInterfaceSettings{Version: 1, Flags: 0x0004, SearchList: search}
		if status := callWindowsSetDNSSettings(windowsProcSetDNSSettings.Addr(), &a.guid, &settings); status != 0 {
			t.Fatalf("set sentinel search list: %d", status)
		}
	}
	apply := func(t *testing.T, a adapter, values []string) windowsOperation {
		t.Helper()
		operation, err := windowsDNSOperation(windowsPowerShellBase(a.name), values)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := system.RunBatch(ctx, a.name, a.luid, []string{operation.apply}, false)
		if err != nil || !reflect.DeepEqual(completed, []int{0}) {
			t.Fatalf("apply DNS: completed=%v err=%v", completed, err)
		}
		return operation
	}
	snapshot := func(t *testing.T, a adapter) map[string]any {
		t.Helper()
		index, err := windowsInterfaceIndexFromLUID(a.luid)
		if err != nil {
			t.Fatal(err)
		}
		script := fmt.Sprintf(`$ErrorActionPreference='Stop';$i=%d;$g='%s';
$servers=@(Get-DnsClientServerAddress -InterfaceIndex $i | Sort-Object AddressFamily | ForEach-Object { [pscustomobject]@{family=[int]$_.AddressFamily;servers=@($_.ServerAddresses | ForEach-Object {[string]$_})} });
$client=Get-DnsClient -InterfaceIndex $i;
$registry=@('Tcpip','Tcpip6' | ForEach-Object { $p=Get-ItemProperty -LiteralPath ('HKLM:\SYSTEM\CurrentControlSet\Services\'+$_+'\Parameters\Interfaces\'+$g) -ErrorAction SilentlyContinue; [pscustomobject]@{family=[string]$_;nameserver=[string]$p.NameServer;domain=[string]$p.Domain;searchlist=[string]$p.SearchList} });
[pscustomobject]@{servers=$servers;suffix=[string]$client.ConnectionSpecificSuffix;register=[bool]$client.RegisterThisConnectionsAddress;use_suffix=[bool]$client.UseSuffixWhenRegistering;registry=$registry}|ConvertTo-Json -Depth 5 -Compress`, index, a.guid.String())
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.WaitDelay = time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("read DNS: %v: %s", err, output)
		}
		var result map[string]any
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("decode DNS: %v: %s", err, output)
		}
		return result
	}
	apply(t, second, []string{"192.0.2.54", "2001:db8::54", "guard.example"})
	setSearchList(t, second, "untouched.example")
	guard := snapshot(t, second)
	for _, test := range []struct {
		name           string
		values         []string
		seedSearchList bool
	}{
		{"dual_stack_suffix", []string{"192.0.2.53", "2001:db8::53", "corp.example"}, true},
		{"suffix_only", []string{"corp.example"}, false},
		{"ipv4_only", []string{"192.0.2.53"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			logSnapshot := func(label string, value map[string]any) {
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("%s: %s", label, encoded)
			}
			// Each case starts without the previous case's search list. In
			// particular, suffix-only must exercise its real effective suffix.
			setSearchList(t, first, "")
			operation := apply(t, first, test.values)
			logSnapshot("PowerShell applied", snapshot(t, first))
			if test.seedSearchList {
				setSearchList(t, first, "untouched.example")
			}
			prepared := snapshot(t, first)
			logSnapshot("before native reset", prepared)
			start := time.Now()
			if err := system.ResetDNS(ctx, compartment, first.luid); err != nil {
				t.Fatal(err)
			}
			t.Logf("native DNS reset: %s", time.Since(start))
			native := snapshot(t, first)
			logSnapshot("after native reset", native)
			if got := snapshot(t, second); !reflect.DeepEqual(got, guard) {
				t.Fatalf("changed second adapter DNS: got=%v want=%v", got, guard)
			}
			if err := system.ResetDNS(ctx, compartment, first.luid); err != nil {
				t.Fatalf("repeated reset: %v", err)
			}
			apply(t, first, test.values)
			if test.seedSearchList {
				setSearchList(t, first, "untouched.example")
			}
			legacyBefore := snapshot(t, first)
			logSnapshot("before legacy reset", legacyBefore)
			if !reflect.DeepEqual(prepared, legacyBefore) {
				t.Fatalf("reset comparison started from different settings: native=%v legacy=%v", prepared, legacyBefore)
			}
			start = time.Now()
			if _, err := system.RunBatch(ctx, first.name, first.luid, []string{operation.undo}, true); err != nil {
				t.Fatal(err)
			}
			t.Logf("legacy PowerShell DNS reset: %s", time.Since(start))
			legacy := snapshot(t, first)
			logSnapshot("after legacy reset", legacy)
			if !reflect.DeepEqual(native, legacy) {
				t.Fatalf("native reset changed semantics: native=%v legacy=%v", native, legacy)
			}
			if native["suffix"] == "corp.example" || strings.Contains(fmt.Sprint(native["servers"]), "192.0.2.53") || strings.Contains(fmt.Sprint(native["servers"]), "2001:db8::53") {
				t.Fatalf("native reset left configured DNS values: %v", native)
			}
		})
	}
}
