package platform

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func TestFreeBSDDefaultRouteIsSplitAroundEndpointRoutes(t *testing.T) {
	cfg := &config.Config{Peers: []config.Peer{{AllowedIPs: []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/0"),
		netip.MustParsePrefix("::/0"),
	}}}}
	operations := freeBSDRouteOperations("wg0", cfg)
	var commands [][]string
	for _, operation := range operations {
		commands = append(commands, operation.apply.args)
	}
	for _, want := range [][]string{
		{"-q", "-n", "add", "-inet", "0.0.0.0/1", "-interface", "wg0"},
		{"-q", "-n", "add", "-inet", "128.0.0.0/1", "-interface", "wg0"},
		{"-q", "-n", "add", "-inet6", "::/1", "-interface", "wg0"},
		{"-q", "-n", "add", "-inet6", "8000::/1", "-interface", "wg0"},
	} {
		if !containsHostCommand(commands, want) {
			t.Errorf("FreeBSD route plan does not contain %#v; got %#v", want, commands)
		}
	}
}

func TestFreeBSDExplicitFIBAndTableOff(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{Table: "2"},
		Peers: []config.Peer{{AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/24"),
		}}},
	}
	operations := freeBSDRouteOperations("wg0", cfg)
	want := []string{"-q", "-n", "add", "-inet", "-fib", "2", "10.0.0.0/24", "-interface", "wg0"}
	if len(operations) != 1 || !slices.Equal(operations[0].apply.args, want) {
		t.Fatalf("explicit FIB plan = %#v", operations)
	}
	cfg.Interface.Table = "off"
	if operations := freeBSDRouteOperations("wg0", cfg); len(operations) != 0 {
		t.Fatalf("Table=off plan = %#v", operations)
	}
}

func containsHostCommand(commands [][]string, want []string) bool {
	for _, command := range commands {
		if slices.Equal(command, want) {
			return true
		}
	}
	return false
}
