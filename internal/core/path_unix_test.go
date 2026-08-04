//go:build linux || freebsd

package core

import "testing"

func TestInterfaceNameFromUnixPathFormats(t *testing.T) {
	for path, want := range map[string]string{
		"/etc/wg-quic/wg0.conf": "wg0",
		"./configs/wg1.conf":    "wg1",
		"configs/wg2.conf":      "wg2",
	} {
		if got := InterfaceName(path, ""); got != want {
			t.Errorf("InterfaceName(%q) = %q, want %q", path, got, want)
		}
	}
}
