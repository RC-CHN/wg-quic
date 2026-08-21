//go:build freebsd

package platform

import "testing"

func TestFreeBSDConfigPathIsProjectSpecific(t *testing.T) {
	if got, want := (freeBSDHost{}).ConfigPath("wg0"), "/usr/local/etc/wg-quic/wg0.conf"; got != want {
		t.Fatalf("ConfigPath(wg0) = %q, want %q", got, want)
	}
	if got, want := (freeBSDHost{}).ControlPath("wg0"), "/var/run/wg-quic/wg0.sock"; got != want {
		t.Fatalf("ControlPath(wg0) = %q, want %q", got, want)
	}
	if got, want := (freeBSDHost{}).ManagementPath("wg0"), "/var/run/wg-quic/wg0.manage.sock"; got != want {
		t.Fatalf("ManagementPath(wg0) = %q, want %q", got, want)
	}
}
