//go:build freebsd

package platform

import (
	"net/netip"
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
