//go:build linux

package quick

import (
	"slices"
	"strings"
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
		commandName, args, err := systemdServiceCommand(test.action, "wg0")
		if err != nil {
			t.Fatal(err)
		}
		if commandName != "systemctl" || !slices.Equal(args, test.args) {
			t.Fatalf("%s command = %q %#v", test.action, commandName, args)
		}
	}
}

func TestOpenWrtServiceCommands(t *testing.T) {
	for _, test := range []struct {
		action string
		args   []string
	}{
		{action: "up", args: []string{"start", "wg0"}},
		{action: "down", args: []string{"stop", "wg0"}},
	} {
		commandName, args, err := openWrtServiceCommand(test.action, "wg0")
		if err != nil {
			t.Fatal(err)
		}
		if commandName != openWrtInitScript || !slices.Equal(args, test.args) {
			t.Fatalf("%s command = %q %#v", test.action, commandName, args)
		}
	}
}

func TestOpenWrtServiceSelectionRequiresPackage(t *testing.T) {
	_, _, err := linuxServiceCommand("up", "wg0", true, false)
	if err == nil || !strings.Contains(err.Error(), "install the wg-quic OpenWrt package") {
		t.Fatalf("missing OpenWrt service error = %v", err)
	}

	commandName, args, err := linuxServiceCommand("up", "wg0", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if commandName != openWrtInitScript || !slices.Equal(args, []string{"start", "wg0"}) {
		t.Fatalf("OpenWrt service command = %q %#v", commandName, args)
	}
}
