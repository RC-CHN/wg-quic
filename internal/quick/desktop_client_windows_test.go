//go:build windows

package quick

import (
	"strings"
	"testing"
)

func TestWindowsDesktopElevationScriptSeparatesStatements(t *testing.T) {
	if !strings.Contains(
		windowsDesktopElevationScript,
		`$ErrorActionPreference = 'Stop'; $process = Start-Process`,
	) {
		t.Fatalf("elevation script does not separate statements: %s", windowsDesktopElevationScript)
	}
	if strings.Contains(windowsDesktopElevationScript, "RedirectStandard") {
		t.Fatalf("elevation script uses an incompatible redirect parameter: %s", windowsDesktopElevationScript)
	}
}

func TestValidateWindowsDesktopRequest(t *testing.T) {
	for _, test := range []struct {
		action string
		name   string
		source string
		valid  bool
	}{
		{action: "up", name: "office", valid: true},
		{action: "import", name: "office", source: `C:\\office.conf`, valid: true},
		{action: "import", name: "office"},
		{action: "up", name: "office", source: `C:\\office.conf`},
		{action: "delete", name: "office"},
		{action: "up", name: `bad/name`},
	} {
		err := validateWindowsDesktopRequest(test.action, test.name, test.source)
		if (err == nil) != test.valid {
			t.Fatalf(
				"validateWindowsDesktopRequest(%q, %q, %q) error=%v, valid=%v",
				test.action,
				test.name,
				test.source,
				err,
				test.valid,
			)
		}
	}
}
