//go:build freebsd

package platform

import "testing"

func TestFreeBSDConfigPathIsProjectSpecific(t *testing.T) {
	if got, want := (freeBSDHost{}).ConfigPath("wg0"), "/usr/local/etc/wg-quic/wg0.conf"; got != want {
		t.Fatalf("ConfigPath(wg0) = %q, want %q", got, want)
	}
}
