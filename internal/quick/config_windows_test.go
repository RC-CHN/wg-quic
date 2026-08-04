//go:build windows

package quick

import (
	"path/filepath"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

func TestResolveConfigWindowsPathFormats(t *testing.T) {
	t.Setenv("ProgramData", `D:\ProgramData`)
	host := platform.Current()
	for _, test := range []struct {
		input string
		name  string
	}{
		{`C:\vpn\wg0.conf`, "wg0"},
		{`C:/vpn/wg1.conf`, "wg1"},
		{`\\server\share\wg2.conf`, "wg2"},
		{`/var/tmp/wg3.conf`, "wg3"},
	} {
		path, name, err := ResolveConfig(test.input, "", host)
		if err != nil {
			t.Errorf("ResolveConfig(%q): %v", test.input, err)
			continue
		}
		if path != test.input || name != test.name {
			t.Errorf("ResolveConfig(%q) = path %q, name %q; want original path and name %q", test.input, path, name, test.name)
		}
	}
	path, name, err := ResolveConfig("office vpn", "", host)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(`D:\ProgramData`, "wg-quic", "interfaces", "office vpn.conf"); path != want || name != "office vpn" {
		t.Fatalf("bare Windows interface = path %q name %q, want path %q", path, name, want)
	}
}
