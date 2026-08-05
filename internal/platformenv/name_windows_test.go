//go:build windows

package platformenv

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

func TestWindowsControlPathsAreDiscoveredFromInstalledConfigs(t *testing.T) {
	programData := t.TempDir()
	t.Setenv("ProgramData", programData)
	configDir := filepath.Join(programData, "wg-quic", "interfaces")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha.conf", "bravo.conf", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := (Paths{}).ControlPaths()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`\\.\pipe\wg-quic-alpha`,
		`\\.\pipe\wg-quic-bravo`,
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("ControlPaths() = %#v, want %#v", paths, want)
	}
}
