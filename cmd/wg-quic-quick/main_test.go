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
