package observe

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

const testPeer = "test-peer-public-key"

type fakeClient struct {
	statuses    []management.Status
	statusCalls int
	events      []telemetry.SessionEvent
}

func (c *fakeClient) Status(context.Context, string) (management.Status, error) {
	index := min(c.statusCalls, len(c.statuses)-1)
	c.statusCalls++
	return c.statuses[index], nil
}

func (c *fakeClient) Events(
	_ context.Context,
	_, streamID string,
	after uint64,
	limit int,
) (telemetry.SessionEventBatch, error) {
	available := min(max(c.statusCalls-1, 0), len(c.events))
	result := telemetry.SessionEventBatch{
		TelemetryVersion: telemetry.SessionEventTelemetryVersion,
		SupervisorEpoch:  "epoch-1", EventStreamID: "stream-1",
		SampledAt: time.Now(), MonotonicElapsedNS: int64(c.statusCalls) * 1_000_000,
		LastSequence: after,
	}
	if available != 0 {
		result.FirstAvailableSequence = 1
	}
	for _, event := range c.events[:available] {
		if event.EventSequence <= after || len(result.Events) >= limit {
			continue
		}
		result.Events = append(result.Events, event)
		result.LastSequence = event.EventSequence
	}
	if streamID != "" && streamID != result.EventStreamID {
		return telemetry.SessionEventBatch{}, boundaryError("unexpected test stream")
	}
	return result, nil
}

func TestCollectorClosesOldGenerationBeforeStartingReplacement(t *testing.T) {
	status1 := testStatus(testSession(1, 1, 12))
	status2 := testStatus(testSession(2, 2, 2))
	status2.RecentSessions = []telemetry.ClosedSessionObservation{{
		TelemetryVersion: telemetry.RecentSessionTelemetryVersion,
		SessionID:        1, SessionGeneration: 1, Role: "outbound", State: "closed",
		Peers: []telemetry.SessionPeerObservation{{
			PublicKey: testPeer, Authenticated: true, Configured: true,
		}},
		Final: true, CloseReason: telemetry.SessionCloseEndpointReplaced,
		FinalStats: telemetry.SessionStats{WireTxBytes: 15, QUICPacketsLost: 3},
	}}
	status3 := testStatus(testSession(2, 2, 5))
	client := &fakeClient{
		statuses: []management.Status{testStatus(testSession(1, 1, 10)), status1, status2, status3},
		events: []telemetry.SessionEvent{
			testEvent(1, 1, 1), testEvent(2, 2, 2), testEvent(3, 0, 0),
		},
	}
	output := filepath.Join(t.TempDir(), "bundle")
	summary, err := Run(context.Background(), client, Options{
		Interface: "wg0", PeerPublicKey: testPeer, Duration: time.Second,
		Interval: minInterval, MaxBytes: 1 << 20, Output: output,
		Version: "test", sampleLimit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Samples != 3 || summary.FailedSamples != 0 || summary.Events != 3 {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := os.Stat(filepath.Join(output, "COMPLETE")); err != nil {
		t.Fatalf("complete marker: %v", err)
	}
	rows := readCSV(t, filepath.Join(output, "peer-telemetry.csv"))
	if len(rows) != 5 {
		t.Fatalf("CSV rows = %d, want header plus 4 samples", len(rows))
	}
	columns := headerMap(rows[0])
	if rows[2][columns["session_id"]] != "1" || rows[2][columns["final"]] != "true" ||
		rows[2][columns["wire_tx_bytes_delta"]] != "3" {
		t.Fatalf("final old-generation row = %#v", rows[2])
	}
	if rows[3][columns["session_id"]] != "2" || rows[3][columns["delta_valid"]] != "false" ||
		rows[3][columns["wire_tx_bytes_delta"]] != "" {
		t.Fatalf("first replacement row crossed generations: %#v", rows[3])
	}
	if rows[4][columns["wire_tx_bytes_delta"]] != "3" {
		t.Fatalf("replacement delta row = %#v", rows[4])
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(output); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o700 {
			t.Fatalf("output directory mode = %v", info.Mode().Perm())
		}
		if info, err := os.Stat(filepath.Join(output, "status.ndjson")); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o600 {
			t.Fatalf("status artifact mode = %v", info.Mode().Perm())
		}
	}
}

func TestSelectSessionRejectsAmbiguousAuthenticatedAssociation(t *testing.T) {
	status := testStatus(testSession(1, 1, 1), testSession(2, 1, 1))
	if _, err := selectSession(status, testPeer, 0); err == nil {
		t.Fatal("ambiguous authenticated sessions were accepted")
	}
	selected, err := selectSession(status, testPeer, 2)
	if err != nil {
		t.Fatal(err)
	}
	if selected.key.SessionID != 2 {
		t.Fatalf("selected session = %#v", selected.key)
	}
}

func TestCollectorConsumesFinalSnapshotWhenTargetDisappears(t *testing.T) {
	closed := testStatus()
	closed.RecentSessions = []telemetry.ClosedSessionObservation{{
		TelemetryVersion: telemetry.RecentSessionTelemetryVersion,
		SessionID:        1, SessionGeneration: 1, Role: "outbound", State: "closed",
		Peers: []telemetry.SessionPeerObservation{{
			PublicKey: testPeer, Authenticated: true, Configured: true,
		}},
		Final: true, CloseReason: telemetry.SessionCloseRemote,
		FinalStats: telemetry.SessionStats{WireTxBytes: 14},
	}}
	client := &fakeClient{statuses: []management.Status{
		testStatus(testSession(1, 1, 10)),
		testStatus(testSession(1, 1, 12)),
		closed,
	}}
	output := filepath.Join(t.TempDir(), "bundle")
	_, err := Run(context.Background(), client, Options{
		Interface: "wg0", PeerPublicKey: testPeer, Duration: time.Second,
		Interval: minInterval, MaxBytes: 1 << 20, Output: output,
		Version: "test", sampleLimit: 2,
	})
	if err == nil {
		t.Fatal("missing target session did not fail the collection")
	}
	if _, err := os.Stat(filepath.Join(output, "INCOMPLETE")); err != nil {
		t.Fatalf("incomplete marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "COMPLETE")); !os.IsNotExist(err) {
		t.Fatalf("unexpected COMPLETE marker error = %v", err)
	}
	rows := readCSV(t, filepath.Join(output, "peer-telemetry.csv"))
	columns := headerMap(rows[0])
	if len(rows) != 4 || rows[2][columns["final"]] != "true" ||
		rows[2][columns["wire_tx_bytes_delta"]] != "2" ||
		rows[3][columns["sample_state"]] != "failed" {
		t.Fatalf("rows after disappearance = %#v", rows)
	}
}

func TestCounterDeltaRejectsRegressionWithinGeneration(t *testing.T) {
	_, err := counterDelta(
		telemetry.SessionStats{QUICPTOCount: 4},
		telemetry.SessionStats{QUICPTOCount: 3},
	)
	if err == nil {
		t.Fatal("counter regression was accepted")
	}
}

func testStatus(sessions ...telemetry.SessionObservation) management.Status {
	return management.Status{
		Interface: "wg0", SupervisorEpoch: "epoch-1", Sessions: sessions,
		Capabilities: []string{
			"session_telemetry_v1", "recent_session_telemetry_v1", "session_events_v1",
		},
		Stats: telemetry.Stats{ReceiveQueueOverflow: telemetry.ReceiveQueueOverflowObservation{
			Supported: true, Source: "linux_so_rxq_ovfl", Platform: "linux",
		}},
	}
}

func testSession(id, generation, wireTxBytes uint64) telemetry.SessionObservation {
	return telemetry.SessionObservation{
		TelemetryVersion: telemetry.SessionTelemetryVersion,
		SessionID:        id, SessionGeneration: generation, Role: "outbound", State: "established",
		Peers: []telemetry.SessionPeerObservation{{
			PublicKey: testPeer, Authenticated: true, Configured: true,
		}},
		Stats: telemetry.SessionStats{WireTxBytes: wireTxBytes},
	}
}

func testEvent(sequence, sessionID, generation uint64) telemetry.SessionEvent {
	return telemetry.SessionEvent{
		TelemetryVersion: telemetry.SessionEventTelemetryVersion,
		EventStreamID:    "stream-1", EventSequence: sequence,
		SessionID: sessionID, SessionGeneration: generation,
		EventType: telemetry.SessionEventPTO, WallTime: time.Now(),
		MonotonicElapsedNS: int64(sequence) * 1_000_000,
	}
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func headerMap(header []string) map[string]int {
	result := make(map[string]int, len(header))
	for index, name := range header {
		result[name] = index
	}
	return result
}
