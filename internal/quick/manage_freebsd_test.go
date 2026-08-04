//go:build freebsd

package quick

import (
	"slices"
	"testing"
)

func TestFreeBSDServiceCommands(t *testing.T) {
	for _, test := range []struct {
		action string
		args   []string
	}{
		{action: "up", args: []string{"wg_quic", "onestart", "wg0"}},
		{action: "down", args: []string{"wg_quic", "onestop", "wg0"}},
	} {
		commandName, args, err := serviceCommand(test.action, "wg0")
		if err != nil {
			t.Fatal(err)
		}
		if commandName != "service" || !slices.Equal(args, test.args) {
			t.Fatalf("%s command = %q %#v", test.action, commandName, args)
		}
	}
}
