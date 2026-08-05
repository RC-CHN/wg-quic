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
