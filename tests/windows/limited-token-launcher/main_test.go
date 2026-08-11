//go:build windows

package main

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestParseCommand(t *testing.T) {
	want := []string{`C:\Program Files\wg-quic\wg-quic.exe`, "show", "office"}
	got, err := parseCommand(append([]string{"--"}, want...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCommand() = %#v, want %#v", got, want)
	}
	if _, err := parseCommand([]string{"--"}); err == nil {
		t.Fatal("parseCommand accepted an empty command")
	}
}

func TestCreateLUARestrictedTokenAndLaunch(t *testing.T) {
	source, err := openLauncherSourceToken()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	enabled, _, err := administratorGroupState(source)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("integration assertion requires an elevated Administrator test token")
	}

	limited, err := createLUARestrictedToken(source)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	if _, err := verifyLUARestrictedToken(limited); err != nil {
		if errors.Is(err, errLUALimitedTokenUnavailable) {
			t.Skipf("host cannot synthesize a genuine UAC-limited token: %v", err)
		}
		t.Fatal(err)
	}

	commandInterpreter := os.Getenv("ComSpec")
	if commandInterpreter == "" {
		commandInterpreter = `C:\Windows\System32\cmd.exe`
	}
	exitCode, err := launchCommandAsUser(limited, []string{
		commandInterpreter, "/d", "/s", "/c", "exit 23",
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 23 {
		t.Fatalf("limited child exit code = %d, want 23", exitCode)
	}
}
