package main

import "testing"

func TestParseRunArgsSupportsDebugAnywhereAfterConfig(t *testing.T) {
	path, options, err := parseRunArgs([]string{
		`C:\ProgramData\wg-quic\interfaces\wg0.conf`,
		"--debug", "--defer-endpoints", "--config-snapshot-stdin", "--name", "wg0", "--fwmark", "17",
	})
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || options.Name != "wg0" || options.FwMark == nil || *options.FwMark != 17 ||
		!options.Debug || !options.DeferEndpoints || options.Snapshot == nil {
		t.Fatalf("unexpected parsed arguments: path=%q options=%+v", path, options)
	}
}

func TestParseRunArgsRejectsMissingOptionValue(t *testing.T) {
	if _, _, err := parseRunArgs([]string{"wg0.conf", "--name"}); err == nil {
		t.Fatal("accepted --name without a value")
	}
}
