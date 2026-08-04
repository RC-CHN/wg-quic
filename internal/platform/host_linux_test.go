//go:build linux

package platform

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func TestLinuxAutomaticDefaultRouteUsesFwmarkTable(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{FwMark: 51820},
		Peers: []config.Peer{{
			AllowedIPs: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.2/32"),
				netip.MustParsePrefix("0.0.0.0/0"),
			},
		}},
	}
	operations, err := linuxRouteOperations("wg0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var commands [][]string
	for _, operation := range operations {
		commands = append(commands, operation.apply.args)
	}
	for _, want := range [][]string{
		{"-4", "route", "add", "10.0.0.2/32", "dev", "wg0"},
		{"-4", "route", "add", "0.0.0.0/0", "dev", "wg0", "table", "51820"},
		{"-4", "rule", "add", "not", "fwmark", "51820", "table", "51820"},
		{"-4", "rule", "add", "table", "main", "suppress_prefixlength", "0"},
	} {
		if !containsCommand(commands, want) {
			t.Errorf("route plan does not contain %#v; got %#v", want, commands)
		}
	}
	if got := commands[len(commands)-1]; !slices.Equal(got, []string{
		"-4", "route", "add", "0.0.0.0/0", "dev", "wg0", "table", "51820",
	}) {
		t.Fatalf("default route must be installed after policy rules, last command = %#v", got)
	}
}

func TestLinuxExplicitAndDisabledRouteTables(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{Table: "123"},
		Peers: []config.Peer{{AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/24"),
		}}},
	}
	operations, err := linuxRouteOperations("wg0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-4", "route", "add", "10.0.0.0/24", "dev", "wg0", "table", "123"}
	if len(operations) != 1 || !slices.Equal(operations[0].apply.args, want) {
		t.Fatalf("explicit table plan = %#v", operations)
	}
	cfg.Interface.Table = "off"
	operations, err = linuxRouteOperations("wg0", cfg)
	if err != nil || len(operations) != 0 {
		t.Fatalf("Table=off plan = %#v, %v", operations, err)
	}
}

func TestLinuxRoutePlanDeduplicatesAllowedIPs(t *testing.T) {
	prefix := netip.MustParsePrefix("10.0.0.0/24")
	cfg := &config.Config{Peers: []config.Peer{
		{AllowedIPs: []netip.Prefix{prefix}},
		{AllowedIPs: []netip.Prefix{prefix}},
	}}
	operations, err := linuxRouteOperations("wg0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("duplicate AllowedIPs created %d route operations", len(operations))
	}
}

func TestLinuxInterfaceNameValidation(t *testing.T) {
	host := linuxHost{}
	if err := host.ValidateInterfaceName("wg-quic0"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "this-interface-name-is-too-long", "bad/name"} {
		if err := host.ValidateInterfaceName(invalid); err == nil {
			t.Errorf("accepted invalid interface name %q", invalid)
		}
	}
}

func TestLinuxConfigPathIsProjectSpecific(t *testing.T) {
	if got, want := (linuxHost{}).ConfigPath("wg0"), "/etc/wg-quic/wg0.conf"; got != want {
		t.Fatalf("ConfigPath(wg0) = %q, want %q", got, want)
	}
	if got, want := (linuxHost{}).ControlPath("wg0"), "/run/wg-quic/wg0.sock"; got != want {
		t.Fatalf("ControlPath(wg0) = %q, want %q", got, want)
	}
}

func containsCommand(commands [][]string, want []string) bool {
	for _, command := range commands {
		if slices.Equal(command, want) {
			return true
		}
	}
	return false
}
