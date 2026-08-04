//go:build windows

package platform

import (
	"path/filepath"
	"testing"
)

func TestWindowsHostPathsAndNames(t *testing.T) {
	t.Setenv("ProgramData", `D:\ProgramData`)
	host := windowsHost{}
	if got, want := host.ConfigPath("wg0"), filepath.Join(`D:\ProgramData`, "wg-quic", "interfaces", "wg0.conf"); got != want {
		t.Fatalf("ConfigPath(wg0) = %q, want %q", got, want)
	}
	if got, want := host.ControlPath("wg0"), `\\.\pipe\wg-quic-wg0`; got != want {
		t.Fatalf("ControlPath(wg0) = %q, want %q", got, want)
	}
	for _, valid := range []string{"wg0", "office vpn"} {
		if err := host.ValidateInterfaceName(valid); err != nil {
			t.Errorf("valid name %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", `bad\name`, "trailing.", "trailing "} {
		if err := host.ValidateInterfaceName(invalid); err == nil {
			t.Errorf("accepted invalid Windows interface name %q", invalid)
		}
	}
}
