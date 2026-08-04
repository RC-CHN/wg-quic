package quick

import (
	"io"
	"slices"
	"strings"
	"testing"
)

func TestCoreCommandCarriesSnapshotOnlyOnStdin(t *testing.T) {
	snapshot := []byte(`{"private_key":"must-not-appear-in-argv"}`)
	cmd, err := coreCommand("wg-quic", coreLaunch{
		ConfigPath: "wg0.conf", Name: "wg0", Snapshot: snapshot,
		DeferEndpoints: true, Debug: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"wg-quic", "run", "wg0.conf", "--name", "wg0",
		"--config-snapshot-stdin", "--defer-endpoints", "--debug",
	}
	if !slices.Equal(cmd.Args, wantArgs) {
		t.Fatalf("core command args = %#v, want %#v", cmd.Args, wantArgs)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "must-not-appear") {
		t.Fatal("core command leaked configuration contents in argv")
	}
	got, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(snapshot) {
		t.Fatalf("core command stdin = %q, want %q", got, snapshot)
	}
}

func TestCoreCommandRequiresSnapshot(t *testing.T) {
	if _, err := coreCommand("wg-quic", coreLaunch{}); err == nil {
		t.Fatal("core command accepted an empty configuration snapshot")
	}
}
