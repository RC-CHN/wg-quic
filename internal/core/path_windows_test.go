//go:build windows

package core

import "testing"

func TestInterfaceNameFromWindowsPathFormats(t *testing.T) {
	for path, want := range map[string]string{
		`C:\vpn\wg0.conf`:         "wg0",
		`C:/vpn/wg1.conf`:         "wg1",
		`\\server\share\wg2.conf`: "wg2",
		`/var/tmp/wg3.conf`:       "wg3",
	} {
		if got := InterfaceName(path, ""); got != want {
			t.Errorf("InterfaceName(%q) = %q, want %q", path, got, want)
		}
	}
}
