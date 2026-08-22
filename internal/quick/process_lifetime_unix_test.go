//go:build linux || freebsd

package quick

import (
	"os"
	"slices"
	"testing"
)

func TestUnixCoreProcessCarriesSupervisorLifetimePipe(t *testing.T) {
	process, err := newUnixCoreProcess(os.Args[0], coreLaunch{
		ConfigPath: "wg0.conf", Name: "wg0", Snapshot: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if process.lifetimeReader != nil {
			_ = process.lifetimeReader.Close()
		}
		process.closeLifetime()
	}()
	if len(process.cmd.ExtraFiles) != 1 || process.cmd.ExtraFiles[0] != process.lifetimeReader {
		t.Fatalf("core extra files = %#v", process.cmd.ExtraFiles)
	}
	if !slices.Contains(process.cmd.Args, "--supervisor-fd") ||
		process.cmd.Args[len(process.cmd.Args)-1] != "3" {
		t.Fatalf("core command does not identify inherited lifetime fd: %q", process.cmd.Args)
	}
}
