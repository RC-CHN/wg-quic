//go:build windows

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
	"golang.org/x/sys/windows"
)

func TestWindowsDNSApplyNativeIntegration(t *testing.T) {
	if os.Getenv("WG_QUIC_TEST_WINDOWS_NETWORK") != "1" {
		t.Skip("requires an elevated isolated Windows guest with wintun.dll")
	}
	if err := windowsProcSetDNSSettings.Find(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	system := windowsNativeNetworkSystem{}
	compartment := system.CurrentCompartmentID()
	type adapter struct {
		name  string
		luid  uint64
		guid  windows.GUID
		index uint32
	}
	create := func(suffix string) adapter {
		t.Helper()
		name := fmt.Sprintf("wgq-dnsapply-%s-%d", suffix, os.Getpid())
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
		index, err := windowsInterfaceIndexFromLUID(luid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := system.ResetDNS(context.Background(), compartment, luid); err != nil {
				t.Error(err)
			}
		})
		return adapter{name, luid, guid, index}
	}
	first, second := create("native"), create("ps")
	seed := func(t *testing.T, a adapter, search string) {
		t.Helper()
		ptr := func(value string) *uint16 {
			p, err := windows.UTF16PtrFromString(value)
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		// Identical non-default starting values expose unwanted overwrites of
		// an omitted family, suffix, or independent search list.
		settings := []windowsDNSInterfaceSettings{
			{Version: 1, Flags: windowsDNSSettingsNameServer | windowsDNSSettingsDomain | 0x0004, NameServer: ptr("192.0.2.90,192.0.2.91"), Domain: ptr("seed.example"), SearchList: ptr(search)},
			{Version: 1, Flags: windowsDNSSettingsNameServer | windowsDNSSettingsIPv6, NameServer: ptr("2001:db8::90,2001:db8::91")},
		}
		for i := range settings {
			if status := callWindowsSetDNSSettings(windowsProcSetDNSSettings.Addr(), &a.guid, &settings[i]); status != 0 {
				t.Fatalf("seed DNS: %d", status)
			}
		}
	}
	snapshot := func(t *testing.T) []map[string]any {
		t.Helper()
		// Convert primitive fields only: serializing live CIM / PSDrive objects
		// can consume enough CPU to distort the very timings under test.
		script := fmt.Sprintf(`$ErrorActionPreference='Stop';
$specs=@(@{i=%d;g='%s'},@{i=%d;g='%s'});
$result=@(foreach($spec in $specs){
$i=$spec.i;$g=$spec.g;
$servers=@(Get-DnsClientServerAddress -InterfaceIndex $i | Sort-Object AddressFamily | ForEach-Object { [pscustomobject]@{family=[int]$_.AddressFamily;servers=@($_.ServerAddresses | ForEach-Object {[string]$_})} });
$client=Get-DnsClient -InterfaceIndex $i;
$registry=@('Tcpip','Tcpip6' | ForEach-Object { $p=Get-ItemProperty -LiteralPath ('HKLM:\SYSTEM\CurrentControlSet\Services\'+$_+'\Parameters\Interfaces\'+$g) -ErrorAction SilentlyContinue; [pscustomobject]@{family=[string]$_;nameserver=[string]$p.NameServer;domain=[string]$p.Domain;searchlist=[string]$p.SearchList} });
[pscustomobject]@{servers=$servers;suffix=[string]$client.ConnectionSpecificSuffix;register=[bool]$client.RegisterThisConnectionsAddress;use_suffix=[bool]$client.UseSuffixWhenRegistering;registry=$registry}
});ConvertTo-Json -InputObject $result -Depth 5 -Compress`, first.index, first.guid.String(), second.index, second.guid.String())
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.WaitDelay = time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("snapshot DNS: %v: %s", err, output)
		}
		var result []map[string]any
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("decode DNS: %v: %s", err, output)
		}
		if len(result) != 2 {
			t.Fatalf("missing snapshots: %s", output)
		}
		return result
	}
	for _, test := range []struct {
		name   string
		values []string
		search string
	}{
		{"mixed_order_suffix", []string{"2001:db8::54", "192.0.2.54", "2001:db8::53", "192.0.2.53", "corp.example"}, ""},
		{"ipv4_only_preserves_suffix", []string{"192.0.2.54", "192.0.2.53"}, "untouched.example"},
		{"ipv6_only_preserves_suffix", []string{"2001:db8::54", "2001:db8::53"}, "untouched.example"},
		{"suffix_only_preserves_servers", []string{"corp.example"}, "untouched.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			seed(t, first, test.search)
			seed(t, second, test.search)
			before := snapshot(t)
			if !reflect.DeepEqual(before[0], before[1]) {
				t.Fatalf("different initial DNS: %v", before)
			}
			encoded, _ := json.Marshal(before[0])
			t.Logf("initial settings: %s", encoded)
			start := time.Now()
			applied, err := system.ApplyDNS(ctx, compartment, first.luid, test.values)
			duration := time.Since(start)
			if err != nil || !applied {
				t.Fatalf("native apply: applied=%v err=%v", applied, err)
			}
			t.Logf("native DNS apply: %s", duration)
			native := snapshot(t)
			if !reflect.DeepEqual(native[1], before[1]) {
				t.Fatalf("native apply changed other adapter: got=%v want=%v", native[1], before[1])
			}
			operation, err := windowsDNSOperation(windowsPowerShellBase(second.name), test.values)
			if err != nil {
				t.Fatal(err)
			}
			start = time.Now()
			completed, err := system.RunBatch(ctx, second.name, second.luid, []string{operation.apply}, false)
			duration = time.Since(start)
			if err != nil || !reflect.DeepEqual(completed, []int{0}) {
				t.Fatalf("PowerShell apply: completed=%v err=%v", completed, err)
			}
			t.Logf("PowerShell reference DNS apply: %s", duration)
			after := snapshot(t)
			for i, label := range []string{"native", "legacy"} {
				encoded, _ := json.Marshal(after[i])
				t.Logf("%s settings: %s", label, encoded)
			}
			if !reflect.DeepEqual(after[0], native[0]) {
				t.Fatalf("reference changed native adapter: got=%v want=%v", after[0], native[0])
			}
			if !reflect.DeepEqual(comparableWindowsDNSApplySnapshot(after[0]), comparableWindowsDNSApplySnapshot(after[1])) {
				t.Fatalf("native DNS apply changed semantics: native=%v legacy=%v", after[0], after[1])
			}
		})
	}
}

func comparableWindowsDNSApplySnapshot(value map[string]any) map[string]any {
	result := maps.Clone(value)
	registry := value["registry"].([]any)
	entries := make([]any, len(registry))
	for i, entry := range registry {
		row := maps.Clone(entry.(map[string]any))
		// The PowerShell provider pads NameServer REG_SZ with extra NULs.
		// Preserve raw snapshots in the log; compare only its logical string.
		// Interior NULs or a nonzero suffix must remain visible differences.
		row["nameserver"] = strings.TrimRight(row["nameserver"].(string), "\x00")
		entries[i] = row
	}
	result["registry"] = entries
	return result
}

func TestWindowsDNSApplyRegistryComparisonPreservesNonPadding(t *testing.T) {
	snapshot := func(server string) map[string]any {
		return map[string]any{"registry": []any{map[string]any{"nameserver": server, "domain": "seed.example"}}}
	}
	plain := comparableWindowsDNSApplySnapshot(snapshot("192.0.2.53"))
	padded := snapshot("192.0.2.53\x00\x00")
	if !reflect.DeepEqual(plain, comparableWindowsDNSApplySnapshot(padded)) {
		t.Fatal("registry string padding changed logical DNS servers")
	}
	if padded["registry"].([]any)[0].(map[string]any)["nameserver"] != "192.0.2.53\x00\x00" {
		t.Fatal("comparison modified the raw evidence")
	}
	for _, invalid := range []string{"192.0.2.53\x00other", "192.0.2.\x0053"} {
		if reflect.DeepEqual(plain, comparableWindowsDNSApplySnapshot(snapshot(invalid))) {
			t.Fatalf("comparison hid non-padding data: %q", invalid)
		}
	}
}
