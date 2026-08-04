//go:build linux

package quick

import (
	"slices"
	"testing"
)

func TestLinuxServiceCommands(t *testing.T) {
	for _, test := range []struct {
		action string
		args   []string
	}{
		{action: "up", args: []string{"start", "wg-quic@wg0.service"}},
		{action: "down", args: []string{"stop", "wg-quic@wg0.service"}},
	} {
		commandName, args, err := serviceCommand(test.action, "wg0")
		if err != nil {
			t.Fatal(err)
		}
		if commandName != "systemctl" || !slices.Equal(args, test.args) {
			t.Fatalf("%s command = %q %#v", test.action, commandName, args)
		}
	}
}
