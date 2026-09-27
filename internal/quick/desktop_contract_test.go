package quick

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

// The same fixtures are consumed by Rust's native mapper and TypeScript's
// display/rate model. Regeneration is an explicit, reviewable schema change.
func TestDesktopSharedStatusContract(t *testing.T) {
	const sampledAt = int64(1790467200000)
	type fixture struct {
		Name    string         `json:"name"`
		Report  DesktopStatus  `json:"report"`
		View    map[string]any `json:"view"`
		Display string         `json:"display"`
	}
	var fixtures []fixture
	for _, display := range []string{"connected", "authenticating", "reconnecting", "activating", "inactive", "unknown"} {
		status := control.Status{Interface: "wg0", State: "up", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", ObservationID: "fixture-runtime", Carrier: "quic", FECMode: "auto", ObfsMode: "salamander", Peers: []control.PeerStatus{{PublicKey: "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", Generation: 1, Session: "established", TransferRx: 1000, TransferTx: 2000}}}
		status.Sessions = []telemetry.SessionObservation{{SessionID: 1, SessionGeneration: 1, State: "established", SampledAt: time.UnixMilli(sampledAt).UTC()}}
		var failure error
		switch display {
		case "connected":
			status.Sessions[0].Peers = []telemetry.SessionPeerObservation{{PublicKey: status.Peers[0].PublicKey, Authenticated: true}}
		case "reconnecting":
			status.Peers[0].Session = "reconnecting"
			status.Sessions = nil
		case "activating":
			status.State = "prepared"
		case "inactive":
			status = control.Status{}
			failure = os.ErrNotExist
		case "unknown":
			status = control.Status{}
			failure = os.ErrPermission
		}
		report := desktopStatusResult("wg0", status, failure)
		report.SampledAt = sampledAt
		view := map[string]any{"name": "wg0", "configPath": "wg0.conf", "running": report.State == "up", "statusState": report.State, "sampledAt": sampledAt}
		if report.Status != nil {
			view["status"] = report.Status
		}
		if report.Failure != nil {
			view["statusCode"] = report.Failure.Code
			view["statusDetail"] = report.Failure.Message
		}
		fixtures = append(fixtures, fixture{Name: display, Report: report, View: view, Display: display})
	}
	encoded, err := json.MarshalIndent(fixtures, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "tests", "fixtures", "desktop", "status.json")
	if os.Getenv("UPDATE_DESKTOP_CONTRACT") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0644); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Git may check text fixtures out with CRLF on Windows. Compare JSON
	// without insignificant whitespace, while retaining every field/value.
	var actualJSON, expectedJSON bytes.Buffer
	if err := json.Compact(&actualJSON, actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&expectedJSON, encoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualJSON.Bytes(), expectedJSON.Bytes()) {
		t.Fatal("Go status contract changed; review and regenerate tests/fixtures/desktop/status.json with UPDATE_DESKTOP_CONTRACT=1")
	}
}
