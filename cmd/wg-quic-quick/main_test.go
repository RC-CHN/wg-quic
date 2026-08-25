package main

import (
	"slices"
	"testing"
	"time"
)

func TestParseQuickRunArgs(t *testing.T) {
	input, name, brokerSafe, err := parseQuickRunArgs([]string{
		"custom.conf", "--name", "wg0", "--broker-safe",
	})
	if err != nil {
		t.Fatal(err)
	}
	if input != "custom.conf" || name != "wg0" || !brokerSafe {
		t.Fatalf(
			"parsed input=%q name=%q brokerSafe=%v",
			input, name, brokerSafe,
		)
	}
}

func TestParseQuickRunArgsRejectsUnknownOption(t *testing.T) {
	if _, _, _, err := parseQuickRunArgs([]string{
		"wg0", "--verbose", "yes",
	}); err == nil {
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

func TestParseDesktopClientArgs(t *testing.T) {
	request, err := parseDesktopClientArgs([]string{
		"import",
		"office",
		`C:\\Users\\me\\office.conf`,
		"--overwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.action != "import" ||
		request.name != "office" ||
		request.source != `C:\\Users\\me\\office.conf` ||
		!request.overwrite {
		t.Fatalf("unexpected desktop request: %#v", request)
	}
}

func TestParseDesktopClientReadArgs(t *testing.T) {
	request, err := parseDesktopClientArgs([]string{"read", "office"})
	if err != nil {
		t.Fatal(err)
	}
	if request.action != "read" || request.name != "office" ||
		request.source != "" || request.overwrite {
		t.Fatalf("unexpected desktop read request: %#v", request)
	}
}

func TestParseDesktopClientRuntimeArgs(t *testing.T) {
	for _, action := range []string{"status", "reload", "refresh-endpoints"} {
		request, err := parseDesktopClientArgs([]string{action, "office"})
		if err != nil {
			t.Fatalf("parse %s: %v", action, err)
		}
		if request.action != action || request.name != "office" ||
			request.source != "" || request.overwrite {
			t.Fatalf("unexpected %s request: %#v", action, request)
		}
	}
}

func TestParseDesktopClientReconcileArgs(t *testing.T) {
	request, err := parseDesktopClientArgs([]string{
		"reconcile", "office", `C:\Users\me\office-next.conf`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.action != "reconcile" || request.name != "office" ||
		request.source != `C:\Users\me\office-next.conf` || request.overwrite {
		t.Fatalf("unexpected desktop reconcile request: %#v", request)
	}
}

func TestParseDesktopClientArgsRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"up"},
		{"up", "wg0", "extra"},
		{"import", "wg0"},
		{"import", "wg0", "source.conf", "--replace"},
		{"restart", "wg0"},
		{"delete"},
		{"delete", "wg0", "extra"},
		{"read", "wg0", "extra"},
		{"reconcile", "wg0"},
		{"reconcile", "wg0", "next.conf", "--overwrite"},
	} {
		if _, err := parseDesktopClientArgs(args); err == nil {
			t.Fatalf("parseDesktopClientArgs(%q) accepted invalid input", args)
		}
	}
}

func TestParseDesktopHelperArgs(t *testing.T) {
	pipePath, err := parseDesktopHelperArgs([]string{`\\.\pipe\wg-quic-desktop-test`})
	if err != nil {
		t.Fatal(err)
	}
	if pipePath != `\\.\pipe\wg-quic-desktop-test` {
		t.Fatalf("parseDesktopHelperArgs() = %q", pipePath)
	}
	for _, args := range [][]string{nil, {""}, {"one", "two"}} {
		if _, err := parseDesktopHelperArgs(args); err == nil {
			t.Fatalf("parseDesktopHelperArgs(%q) accepted invalid input", args)
		}
	}
}

func TestParseDesktopImportArgs(t *testing.T) {
	name, source, overwrite, err := parseDesktopImportArgs([]string{
		"office",
		"office.conf",
		"--overwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "office" || source != "office.conf" || !overwrite {
		t.Fatalf(
			"parseDesktopImportArgs() = (%q, %q, %v)",
			name,
			source,
			overwrite,
		)
	}
}

func TestParseDesktopImportArgsRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"office"},
		{"", "office.conf"},
		{"office", "office.conf", "--replace"},
	} {
		if _, _, _, err := parseDesktopImportArgs(args); err == nil {
			t.Fatalf("parseDesktopImportArgs(%q) accepted invalid input", args)
		}
	}
}

func TestParseRuntimeReconcileAndRefreshArgs(t *testing.T) {
	reconcileArgs, err := parseRuntimeCommand("reconcile", []string{
		"wg0", "/run/wg-quic/candidate.conf",
		"--expected-epoch", "epoch", "--expected-generation", "7",
		"--request-id", "request", "--json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconcileArgs.name != "wg0" ||
		reconcileArgs.candidatePath != "/run/wg-quic/candidate.conf" ||
		reconcileArgs.expectedEpoch != "epoch" || reconcileArgs.expectedGeneration != 7 ||
		reconcileArgs.requestID != "request" || !reconcileArgs.jsonOutput {
		t.Fatalf("reconcile args = %#v", reconcileArgs)
	}
	refreshArgs, err := parseRuntimeCommand("refresh-endpoints", []string{
		"wg0", "--peer", "public-key", "--json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshArgs.name != "wg0" || refreshArgs.peer != "public-key" || !refreshArgs.jsonOutput {
		t.Fatalf("refresh args = %#v", refreshArgs)
	}
	eventArgs, err := parseRuntimeCommand("events", []string{
		"wg0", "--event-stream-id", "stream", "--after-sequence", "7",
		"--limit", "32", "--json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventArgs.eventStreamID != "stream" || eventArgs.afterSequence != 7 ||
		eventArgs.eventLimit != 32 || !eventArgs.jsonOutput {
		t.Fatalf("event args = %#v", eventArgs)
	}
}

func TestParseRuntimeCommandRejectsMissingCASAndRequestID(t *testing.T) {
	tests := [][]string{
		{"reconcile", "wg0", "candidate.conf"},
		{"reconcile", "wg0", "candidate.conf", "--expected-epoch", "epoch"},
		{"transaction-status", "wg0"},
		{"refresh-endpoints", "wg0", "--peer"},
		{"events", "wg0", "--after-sequence", "1"},
		{"events", "wg0", "--limit", "1025"},
	}
	for _, test := range tests {
		if _, err := parseRuntimeCommand(test[0], test[1:]); err == nil {
			t.Fatalf("accepted invalid runtime command %q", slices.Clone(test))
		}
	}
}

func TestParseCollectArgs(t *testing.T) {
	args, err := parseCollectArgs([]string{
		"wg0", "--peer", "public-key", "--session-id", "7",
		"--duration", "45s", "--interval", "250ms",
		"--max-bytes", "32MiB", "--output", "/var/lib/wg-quic/traces/trial",
	})
	if err != nil {
		t.Fatal(err)
	}
	if args.name != "wg0" || args.peer != "public-key" || args.sessionID != 7 ||
		args.duration != 45*time.Second || args.interval != 250*time.Millisecond ||
		args.maxBytes != 32<<20 || args.output != "/var/lib/wg-quic/traces/trial" {
		t.Fatalf("collect args = %#v", args)
	}
}

func TestParseCollectArgsRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"wg0", "--output", "trial"},
		{"wg0", "--peer", "key"},
		{"wg0", "--peer", "key", "--session-id", "0", "--output", "trial"},
		{"wg0", "--peer", "key", "--max-bytes", "lots", "--output", "trial"},
		{"wg0", "--peer", "key", "--unknown", "value", "--output", "trial"},
	} {
		if _, err := parseCollectArgs(args); err == nil {
			t.Fatalf("parseCollectArgs(%q) accepted invalid input", args)
		}
	}
}
