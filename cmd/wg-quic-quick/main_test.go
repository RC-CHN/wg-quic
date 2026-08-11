package main

import "testing"

func TestParseQuickRunArgs(t *testing.T) {
	input, name, err := parseQuickRunArgs([]string{"custom.conf", "--name", "wg0"})
	if err != nil {
		t.Fatal(err)
	}
	if input != "custom.conf" || name != "wg0" {
		t.Fatalf("parsed input=%q name=%q", input, name)
	}
}

func TestParseQuickRunArgsRejectsUnknownOption(t *testing.T) {
	if _, _, err := parseQuickRunArgs([]string{"wg0", "--verbose", "yes"}); err == nil {
		t.Fatal("accepted unknown option")
	}
}

func TestParseQuickDownArgs(t *testing.T) {
	for _, test := range []struct {
		args       []string
		wantName   string
		wantRepair bool
	}{
		{args: []string{"wg0"}, wantName: "wg0"},
		{args: []string{"wg0", "--repair"}, wantName: "wg0", wantRepair: true},
	} {
		name, repair, err := parseQuickDownArgs(test.args)
		if err != nil {
			t.Fatalf("parseQuickDownArgs(%q): %v", test.args, err)
		}
		if name != test.wantName || repair != test.wantRepair {
			t.Fatalf(
				"parseQuickDownArgs(%q) = (%q, %v), want (%q, %v)",
				test.args, name, repair, test.wantName, test.wantRepair,
			)
		}
	}
}

func TestParseQuickDownArgsRejectsUnknownOptions(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"wg0", "--force"},
		{"--repair", "wg0"},
		{"wg0", "--repair", "extra"},
	} {
		if _, _, err := parseQuickDownArgs(args); err == nil {
			t.Fatalf("parseQuickDownArgs(%q) accepted invalid input", args)
		}
	}
}

func TestParseDesktopClientArgs(t *testing.T) {
	request, err := parseDesktopClientArgs([]string{
		"import",
		"office",
		`C:\\Users\\me\\office.conf`,
		"--overwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.action != "import" ||
		request.name != "office" ||
		request.source != `C:\\Users\\me\\office.conf` ||
		!request.overwrite {
		t.Fatalf("unexpected desktop request: %#v", request)
	}
}

func TestParseDesktopClientArgsRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"up"},
		{"up", "wg0", "extra"},
		{"import", "wg0"},
		{"import", "wg0", "source.conf", "--replace"},
		{"delete", "wg0"},
	} {
		if _, err := parseDesktopClientArgs(args); err == nil {
			t.Fatalf("parseDesktopClientArgs(%q) accepted invalid input", args)
		}
	}
}

func TestParseDesktopHelperArgs(t *testing.T) {
	pipePath, err := parseDesktopHelperArgs([]string{`\\.\pipe\wg-quic-desktop-test`})
	if err != nil {
		t.Fatal(err)
	}
	if pipePath != `\\.\pipe\wg-quic-desktop-test` {
		t.Fatalf("parseDesktopHelperArgs() = %q", pipePath)
	}
	for _, args := range [][]string{nil, {""}, {"one", "two"}} {
		if _, err := parseDesktopHelperArgs(args); err == nil {
			t.Fatalf("parseDesktopHelperArgs(%q) accepted invalid input", args)
		}
	}
}

func TestParseDesktopImportArgs(t *testing.T) {
	name, source, overwrite, err := parseDesktopImportArgs([]string{
		"office",
		"office.conf",
		"--overwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "office" || source != "office.conf" || !overwrite {
		t.Fatalf(
			"parseDesktopImportArgs() = (%q, %q, %v)",
			name,
			source,
			overwrite,
		)
	}
}

func TestParseDesktopImportArgsRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"office"},
		{"", "office.conf"},
		{"office", "office.conf", "--replace"},
	} {
		if _, _, _, err := parseDesktopImportArgs(args); err == nil {
			t.Fatalf("parseDesktopImportArgs(%q) accepted invalid input", args)
		}
	}
}
