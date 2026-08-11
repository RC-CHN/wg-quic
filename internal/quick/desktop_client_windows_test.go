//go:build windows

package quick

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

func TestWindowsDesktopElevationScriptSeparatesStatements(t *testing.T) {
	if !strings.Contains(
		windowsDesktopElevationScript,
		`$ErrorActionPreference = 'Stop'; $pipePath = $env:WG_QUIC_DESKTOP_PIPE; $process = Start-Process`,
	) {
		t.Fatalf("elevation script does not separate statements: %s", windowsDesktopElevationScript)
	}
	if !strings.Contains(
		windowsDesktopElevationScript,
		`-ArgumentList @('desktop-helper', $pipePath)`,
	) {
		t.Fatalf("elevation script does not pass the IPC pipe explicitly: %s", windowsDesktopElevationScript)
	}
	for _, inheritedRequest := range []string{
		"WG_QUIC_DESKTOP_ACTION",
		"WG_QUIC_DESKTOP_NAME",
		"WG_QUIC_DESKTOP_SOURCE",
		"WG_QUIC_DESKTOP_OVERWRITE",
	} {
		if strings.Contains(windowsDesktopElevationScript, inheritedRequest) {
			t.Fatalf(
				"elevation script still relies on inherited request environment %s",
				inheritedRequest,
			)
		}
	}
	if strings.Contains(windowsDesktopElevationScript, "RedirectStandard") {
		t.Fatalf("elevation script uses an incompatible redirect parameter: %s", windowsDesktopElevationScript)
	}
}

func TestWindowsDesktopRequestAndResultShareDuplexPipe(t *testing.T) {
	if windows.NewLazySystemDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil {
		t.Skip("the native named-pipe transport is covered by Windows CI")
	}
	pipePath, err := randomWindowsDesktopPipePath()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := (&namedpipe.ListenConfig{
		InputBufferSize:  64 * 1024,
		OutputBufferSize: 64 * 1024,
	}).Listen(pipePath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	wantRequest := windowsDesktopRequest{
		Action: "import", Name: "office",
		Source: `C:\\Users\\me\\office.conf`, Overwrite: true,
	}
	resultChannel := make(chan windowsDesktopResult, 1)
	errorChannel := make(chan error, 1)
	go exchangeWindowsDesktopRequest(
		listener, wantRequest, resultChannel, errorChannel,
	)

	connection, err := openWindowsDesktopPipe(pipePath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	gotRequest, err := readWindowsDesktopRequest(connection)
	if err != nil {
		t.Fatal(err)
	}
	if gotRequest != wantRequest {
		t.Fatalf("desktop request = %#v, want %#v", gotRequest, wantRequest)
	}
	wantResult := windowsDesktopResult{Success: true, Message: "completed"}
	if err := json.NewEncoder(connection).Encode(wantResult); err != nil {
		t.Fatal(err)
	}

	select {
	case gotResult := <-resultChannel:
		if gotResult != wantResult {
			t.Fatalf("desktop result = %#v, want %#v", gotResult, wantResult)
		}
	case err := <-errorChannel:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for desktop duplex exchange")
	}
}

func TestWindowsDesktopRequestAndResultShareDuplexConnection(t *testing.T) {
	client, helper := net.Pipe()
	defer client.Close()
	defer helper.Close()

	wantRequest := windowsDesktopRequest{
		Action: "import", Name: "office",
		Source: `C:\\Users\\me\\office.conf`, Overwrite: true,
	}
	wantResult := windowsDesktopResult{Success: true, Message: "completed"}
	helperResult := make(chan error, 1)
	go func() {
		gotRequest, err := readWindowsDesktopRequest(helper)
		if err == nil && gotRequest != wantRequest {
			err = fmt.Errorf(
				"desktop request = %#v, want %#v", gotRequest, wantRequest,
			)
		}
		if err == nil {
			err = json.NewEncoder(helper).Encode(wantResult)
		}
		helperResult <- err
	}()

	gotResult, err := exchangeWindowsDesktopConnection(client, wantRequest)
	if err != nil {
		t.Fatal(err)
	}
	if gotResult != wantResult {
		t.Fatalf("desktop result = %#v, want %#v", gotResult, wantResult)
	}
	if err := <-helperResult; err != nil {
		t.Fatal(err)
	}
}

func TestValidateWindowsDesktopRequest(t *testing.T) {
	for _, test := range []struct {
		action string
		name   string
		source string
		valid  bool
	}{
		{action: "up", name: "office", valid: true},
		{action: "import", name: "office", source: `C:\\office.conf`, valid: true},
		{action: "import", name: "office"},
		{action: "up", name: "office", source: `C:\\office.conf`},
		{action: "delete", name: "office"},
		{action: "up", name: `bad/name`},
	} {
		err := validateWindowsDesktopRequest(test.action, test.name, test.source)
		if (err == nil) != test.valid {
			t.Fatalf(
				"validateWindowsDesktopRequest(%q, %q, %q) error=%v, valid=%v",
				test.action,
				test.name,
				test.source,
				err,
				test.valid,
			)
		}
	}
}

func TestWindowsDesktopLauncherErrorNamesFailedHandshake(t *testing.T) {
	err := windowsDesktopLauncherError("exit status 1", nil)
	if !strings.Contains(err.Error(), "before completing the IPC handshake") {
		t.Fatalf("launcher error does not identify the failed handshake: %v", err)
	}
	if err := windowsDesktopLauncherError("operation canceled by the user", nil); err.Error() != "administrator approval was canceled" {
		t.Fatalf("canceled elevation error = %q", err)
	}
}

func TestWindowsDesktopHelperRejectsUnexpectedOverwrite(t *testing.T) {
	err := runWindowsDesktopHelper(context.Background(), windowsDesktopRequest{
		Action: "up", Name: "office", Overwrite: true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not accept overwrite") {
		t.Fatalf("unexpected overwrite error = %v", err)
	}
}

func TestWindowsDesktopRequestDeadlineRejectsExpiredAndClampsFuture(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	if _, err := windowsDesktopRequestDeadline(
		windowsDesktopRequest{}, now,
	); err == nil {
		t.Fatal("accepted a desktop request without a deadline")
	}
	if _, err := windowsDesktopRequestDeadline(windowsDesktopRequest{
		DeadlineUnixMillis: now.UnixMilli(),
	}, now); err == nil {
		t.Fatal("accepted an expired desktop request deadline")
	}
	deadline, err := windowsDesktopRequestDeadline(windowsDesktopRequest{
		DeadlineUnixMillis: now.Add(24 * time.Hour).UnixMilli(),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(windowsDesktopOperationTimeout); !deadline.Equal(want) {
		t.Fatalf("clamped deadline = %s, want %s", deadline, want)
	}
	requested := now.Add(30 * time.Second)
	received := now.Add(20 * time.Second)
	deadline, err = windowsDesktopRequestDeadline(windowsDesktopRequest{
		DeadlineUnixMillis: requested.UnixMilli(),
	}, received)
	if err != nil {
		t.Fatal(err)
	}
	if !deadline.Equal(requested) {
		t.Fatalf(
			"helper reset the client deadline after UAC: got %s, want %s",
			deadline, requested,
		)
	}
}

func TestWindowsDesktopHelperDeadlineDoesNotWaitForBlockedOperation(t *testing.T) {
	release := make(chan struct{})
	deadline := time.Now().Add(40 * time.Millisecond)
	started := time.Now()
	err := runWindowsDesktopHelperUntilDeadline(
		context.Background(),
		windowsDesktopRequest{Action: "check", Name: "office"},
		deadline,
		func(context.Context, windowsDesktopRequest) error {
			<-release
			return nil
		},
	)
	close(release)
	if err == nil || !strings.Contains(err.Error(), "operation deadline expired") {
		t.Fatalf("blocked operation deadline error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("blocked operation returned after %s", elapsed)
	}
}

func TestWindowsDesktopResultGraceIsBounded(t *testing.T) {
	if windowsDesktopResultGrace != 5*time.Second {
		t.Fatalf("desktop result grace = %s", windowsDesktopResultGrace)
	}
}
