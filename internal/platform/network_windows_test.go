package platform

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func TestWindowsNetworkPlanLeavesEndpointRoutingToManager(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{
			Addresses: []netip.Prefix{netip.MustParsePrefix("10.77.0.1/24")},
			DNS:       []string{"1.1.1.1", "corp.example"},
		},
		Peers: []config.Peer{{
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
	}
	operations, err := windowsNetworkOperations("wg-quic' test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) < 4 {
		t.Fatalf("network plan has only %d operations", len(operations))
	}
	var defaultIndex = -1
	for i, operation := range operations {
		if strings.Contains(operation.apply, "0.0.0.0/0") &&
			strings.Contains(operation.apply, "New-NetRoute -InterfaceIndex $ifIndex") {
			defaultIndex = i
		}
	}
	if defaultIndex < 0 {
		t.Fatal("network plan does not install the tunnel default route")
	}
	if !strings.Contains(operations[0].apply, "wg-quic'' test") {
		t.Fatal("interface name was not safely PowerShell-quoted")
	}
	for _, operation := range operations {
		if strings.Contains(operation.apply, "Find-NetRoute") ||
			strings.Contains(operation.apply, "42771") {
			t.Fatalf("ordinary network operation owns endpoint routing: %s", operation.apply)
		}
	}
	if !strings.Contains(operations[1].apply, "NlMtuBytes 1280") {
		t.Fatalf("Windows IP-interface MTU did not use the central default: %s", operations[1].apply)
	}
	last := operations[len(operations)-1]
	if !strings.Contains(last.apply, "Set-DnsClientServerAddress") ||
		!strings.Contains(last.apply, "Set-DnsClient") ||
		last.undo == "" {
		t.Fatalf("DNS operation is incomplete: %#v", last)
	}
}

func TestWindowsNetworkPlanHonorsTableOff(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{Table: "off"},
		Peers: []config.Peer{{
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
	}
	operations, err := windowsNetworkOperations("wg0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if strings.Contains(operation.apply, "New-NetRoute") {
			t.Fatalf("Table=off plan installs a route: %s", operation.apply)
		}
	}
}

func TestWindowsNetworkPlanRejectsUnsupportedTableAndDNSDomains(t *testing.T) {
	cfg := &config.Config{Interface: config.Interface{Table: "123"}}
	if _, err := windowsNetworkOperations("wg0", cfg); err == nil {
		t.Fatal("explicit Windows route table was silently accepted")
	}
	cfg.Interface.Table = ""
	cfg.Interface.DNS = []string{"one.example", "two.example"}
	if _, err := windowsNetworkOperations("wg0", cfg); err == nil {
		t.Fatal("multiple Windows DNS suffixes were silently accepted")
	}
}
