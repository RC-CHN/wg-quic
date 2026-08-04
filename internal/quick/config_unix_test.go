//go:build linux || freebsd

package quick

import (
	"testing"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

func TestResolveConfigUnixPathFormats(t *testing.T) {
	host := platform.Current()
	for _, test := range []struct {
		input string
		name  string
	}{
		{"/etc/wg-quic/wg0.conf", "wg0"},
		{"./configs/wg1.conf", "wg1"},
		{"configs/wg2.conf", "wg2"},
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
}
