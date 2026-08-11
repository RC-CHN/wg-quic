//go:build windows

package main

import (
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

func TestOpenLimitedTokenAndLaunch(t *testing.T) {
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
	limited, tokenSource, err := openLauncherLimitedToken(source)
	if err != nil {
		elevationType, elevationErr := launcherTokenElevationType(source)
		if elevationErr == nil && elevationType == tokenElevationTypeDefault {
			t.Skipf("host has no authentic linked UAC token pair: %v", err)
		}
		t.Fatal(err)
	}
	defer limited.Close()
	t.Logf("using limited token from %s", tokenSource)
	if _, err := verifyLUARestrictedToken(limited); err != nil {
		t.Fatal(err)
	}

	commandInterpreter := os.Getenv("ComSpec")
	if commandInterpreter == "" {
		commandInterpreter = `C:\Windows\System32\cmd.exe`
	}
	t.Setenv("WG_QUIC_LIMITED_TOKEN_MARKER", "inherited")
	exitCode, err := launchCommandWithToken(limited, []string{
		commandInterpreter,
		"/d",
		"/s",
		"/c",
		`if defined WG_QUIC_LIMITED_TOKEN_MARKER (exit /b 23) else (exit /b 24)`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 23 {
		t.Fatalf("limited child exit code = %d, want 23", exitCode)
	}
}
