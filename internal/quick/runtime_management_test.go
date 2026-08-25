package quick

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

func testQuickKey(value byte) string {
	key := make([]byte, 32)
	for index := range key {
		key[index] = value
	}
	return base64.StdEncoding.EncodeToString(key)
}

type fakeEndpointRefresher struct {
	allCalls  int
	peerCalls []string
	err       error
}

type endpointStatusFixture []endpoint.Status

func (f endpointStatusFixture) Status() []endpoint.Status { return f }

type telemetryCoreClient struct {
	*fakePeerSetClient
	status control.Status
}

func (c *telemetryCoreClient) Status() (control.Status, error) { return c.status, nil }

func (f *fakeEndpointRefresher) RefreshAll(context.Context) error {
	f.allCalls++
	return f.err
}

func (f *fakeEndpointRefresher) RefreshPeer(_ context.Context, publicKey string) error {
	f.peerCalls = append(f.peerCalls, publicKey)
	return f.err
}

func testRuntimeManagement(t *testing.T, refresher endpointRefresher) *runtimeManagement {
	t.Helper()
	initial, err := reconcile.FromConfig(&config.Config{
		Interface: config.Interface{PrivateKey: testQuickKey(1)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := reconcile.NewCoordinator(
		context.Background(), initial,
		reconcile.ExecuteFunc(func(context.Context, reconcile.Transaction) reconcile.ApplyResult {
			return reconcile.ApplyResult{State: reconcile.StateCommitted}
		}),
		reconcile.Options{Epoch: "epoch", FingerprintKey: []byte("0123456789abcdef")},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	return &runtimeManagement{
		name: "wg0", coordinator: coordinator, refresher: refresher,
		canonicalDigest: initial.Digest(),
	}
}

func TestRuntimeManagementAdvertisesOnlyActiveCapabilities(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	response := runtime.handle(context.Background(), management.Request{
		Operation: management.OperationStatus, Interface: "wg0",
	})
	if response.Status == nil || response.Status.SupervisorEpoch != "epoch" ||
		response.Status.DesiredGeneration != 1 || response.Status.PersistentDrift {
		t.Fatalf("status response = %#v", response)
	}
	if !slices.Equal(response.Status.Capabilities, []string{
		"management_protocol_v1", "endpoint_refresh_v1",
	}) {
		t.Fatalf("capabilities = %#v", response.Status.Capabilities)
	}
	response = runtime.handle(context.Background(), management.Request{
		Operation: management.OperationReconcile, Interface: "wg0",
	})
	if response.Failure == nil || response.Failure.Code != "unsupported_capability" {
		t.Fatalf("reconcile response = %#v", response)
	}
}

func TestRuntimeManagementPreservesLiveEndpointAndActivityCompatibility(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	runtime.endpointStatus = endpointStatusFixture{{
		PublicKey: "peer", ConfiguredEndpoint: "peer.example:443",
		SelectedEndpoint: "192.0.2.10:443", Generation: 4,
	}}
	peers := runtime.peerStatus(&control.Status{Peers: []control.PeerStatus{{
		PublicKey: "peer", Endpoint: "198.51.100.27:43192",
		SelectedEndpoint: "192.0.2.10:443", Generation: 4,
		LastRx: 20, LastTx: 19, LastActivity: 20, LastDirection: "received",
	}}})
	if len(peers) != 1 || peers[0].Endpoint != "198.51.100.27:43192" ||
		peers[0].CurrentEndpoint != peers[0].Endpoint ||
		peers[0].SelectedEndpoint != "192.0.2.10:443" ||
		peers[0].LastActivity != 20 || peers[0].LastDirection != "received" {
		t.Fatalf("management peer status = %#v", peers)
	}
}

func TestRuntimeManagementForwardsPortableSessionTelemetry(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	runtime.core = &telemetryCoreClient{
		fakePeerSetClient: &fakePeerSetClient{},
		status: control.Status{
			Capabilities:            []string{"session_telemetry_v1", "recent_session_telemetry_v1"},
			SessionTelemetryOmitted: 3,
			RecentSessionsEvicted:   4,
			Sessions: []telemetry.SessionObservation{{
				TelemetryVersion: telemetry.SessionTelemetryVersion,
				SessionID:        9, SessionGeneration: 2, Role: "outbound",
			}},
			RecentSessions: []telemetry.ClosedSessionObservation{{
				TelemetryVersion: telemetry.RecentSessionTelemetryVersion,
				SessionID:        8, SessionGeneration: 1, State: "closed", Final: true,
			}},
		},
	}
	status := runtime.status()
	if !slices.Contains(status.Capabilities, "session_telemetry_v1") ||
		!slices.Contains(status.Capabilities, "recent_session_telemetry_v1") ||
		len(status.Sessions) != 1 || status.Sessions[0].SessionID != 9 ||
		status.Sessions[0].SessionGeneration != 2 || status.SessionTelemetryOmitted != 3 ||
		len(status.RecentSessions) != 1 || status.RecentSessions[0].SessionID != 8 ||
		status.RecentSessionsEvicted != 4 {
		t.Fatalf("forwarded session telemetry = %#v", status)
	}
	if !slices.Contains(runtime.capabilities(), "session_telemetry_v1") {
		t.Fatalf("request capability set = %#v", runtime.capabilities())
	}
}

func TestMergeRuntimeRecoveryStatusPreservesDiagnostics(t *testing.T) {
	first := recoveryStatusFixture{status: platform.RecoveryStatus{
		State: "degraded", RetainedAmbiguousObjects: 2, Message: "two routes retained",
	}}
	second := recoveryStatusFixture{status: platform.RecoveryStatus{
		State: "clean", RetainedAmbiguousObjects: 0,
	}}
	status := mergeRuntimeRecoveryStatus(first, second, struct{}{})
	if status.State != "degraded" || status.RetainedAmbiguousObjects != 2 ||
		status.Message != "two routes retained" {
		t.Fatalf("merged recovery status = %#v", status)
	}
}

type recoveryStatusFixture struct{ status platform.RecoveryStatus }

func (f recoveryStatusFixture) RecoveryStatus() platform.RecoveryStatus { return f.status }

func TestRuntimeManagementEnforcesRequiredCapabilitiesBeforeDispatch(t *testing.T) {
	refresher := &fakeEndpointRefresher{}
	runtime := testRuntimeManagement(t, refresher)
	response := runtime.handle(context.Background(), management.Request{
		Operation: management.OperationRefreshEndpoints, Interface: "wg0",
		RequiredCapabilities: []string{"peer_reconcile_v1"},
	})
	if response.Failure == nil || response.Failure.Code != "unsupported_capability" ||
		refresher.allCalls != 0 {
		t.Fatalf("missing required capability response = %#v, calls=%d", response, refresher.allCalls)
	}
	response = runtime.handle(context.Background(), management.Request{
		Operation: management.OperationRefreshEndpoints, Interface: "wg0",
		RequiredCapabilities: []string{"endpoint_refresh_v1"},
	})
	if response.OperationResult == nil || refresher.allCalls != 1 {
		t.Fatalf("available required capability response = %#v, calls=%d", response, refresher.allCalls)
	}
}

func TestRuntimeManagementRefreshesOneOrAllPeers(t *testing.T) {
	refresher := &fakeEndpointRefresher{}
	runtime := testRuntimeManagement(t, refresher)
	response := runtime.handle(context.Background(), management.Request{
		Operation: management.OperationRefreshEndpoints, Interface: "wg0",
	})
	if response.OperationResult == nil || refresher.allCalls != 1 {
		t.Fatalf("refresh-all response = %#v, calls = %d", response, refresher.allCalls)
	}
	response = runtime.handle(context.Background(), management.Request{
		Operation: management.OperationRefreshEndpoints,
		Interface: "wg0", PublicKey: "peer-key",
	})
	if response.OperationResult == nil || !slices.Equal(refresher.peerCalls, []string{"peer-key"}) {
		t.Fatalf("refresh-peer response = %#v, calls = %#v", response, refresher.peerCalls)
	}
	refresher.err = errors.New("DNS failed")
	response = runtime.handle(context.Background(), management.Request{
		Operation: management.OperationRefreshEndpoints, Interface: "wg0",
	})
	if response.Failure == nil || response.Failure.Code != "endpoint_resolution_failed" ||
		!response.Failure.Retryable {
		t.Fatalf("refresh failure = %#v", response)
	}
}

func TestRuntimeManagementRejectsWrongInterfaceAndUnknownTransaction(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	response := runtime.handle(context.Background(), management.Request{
		Operation: management.OperationStatus, Interface: "wg1",
	})
	if response.Failure == nil || response.Failure.Code != "validation_failed" {
		t.Fatalf("wrong-interface response = %#v", response)
	}
	response = runtime.handle(context.Background(), management.Request{
		Operation: management.OperationTransactionStatus,
		Interface: "wg0", RequestID: "missing",
	})
	if response.Failure == nil || response.Failure.Code != "unknown_request_id" {
		t.Fatalf("unknown-transaction response = %#v", response)
	}
}

func TestRuntimeManagementSubmitsSecureCandidateAndTracksPersistentDrift(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	runtime.reconcileActive = true
	runtime.canonicalPath = "/protected/wg0.conf"
	runtime.loadConfig = func(path string) (*config.Config, error) {
		if path == runtime.canonicalPath {
			return &config.Config{
				Interface: config.Interface{PrivateKey: testQuickKey(1)},
			}, nil
		}
		if path != "/protected/candidate.conf" {
			t.Fatalf("configuration path = %q", path)
		}
		return &config.Config{
			Interface: config.Interface{PrivateKey: testQuickKey(1)},
			Peers: []config.Peer{{
				PublicKey: testQuickKey(2),
			}},
		}, nil
	}
	response := runtime.handle(context.Background(), management.Request{
		Operation: management.OperationReconcile, Interface: "wg0",
		ExpectedEpoch: "epoch", ExpectedGeneration: 1, RequestID: "request-1",
		CandidatePath: "/protected/candidate.conf",
	})
	if response.Result == nil || response.Result.State != reconcile.StateCommitted ||
		response.Result.Generation != 2 {
		t.Fatalf("reconcile response = %#v", response)
	}
	status := runtime.status()
	if !status.PersistentDrift || status.DesiredDigest == status.CanonicalDigest ||
		!slices.Contains(status.Capabilities, "peer_reconcile_v1") {
		t.Fatalf("post-candidate status = %#v", status)
	}
}

func TestRuntimeManagementStatusRefreshesCanonicalDriftAndReportsParseFailure(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	runtime.canonicalPath = "/protected/wg0.conf"
	runtime.loadConfig = func(string) (*config.Config, error) {
		return nil, errors.New("canonical file is malformed")
	}
	status := runtime.status()
	if !status.PersistentDrift || status.CanonicalError != "canonical file is malformed" {
		t.Fatalf("canonical failure status = %#v", status)
	}
	runtime.loadConfig = func(string) (*config.Config, error) {
		return &config.Config{
			Interface: config.Interface{PrivateKey: testQuickKey(1)},
		}, nil
	}
	status = runtime.status()
	if status.PersistentDrift || status.CanonicalError != "" {
		t.Fatalf("recovered canonical status = %#v", status)
	}
}

func TestRuntimeManagementReloadUpdatesCanonicalDigestBeforeCASResult(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	runtime.reconcileActive = true
	runtime.canonicalPath = "/protected/wg0.conf"
	runtime.loadConfig = func(path string) (*config.Config, error) {
		if path != runtime.canonicalPath {
			t.Fatalf("reload path = %q", path)
		}
		return &config.Config{
			Interface: config.Interface{PrivateKey: testQuickKey(1)},
			Peers:     []config.Peer{{PublicKey: testQuickKey(3)}},
		}, nil
	}
	response := runtime.handle(context.Background(), management.Request{
		Operation: management.OperationReload, Interface: "wg0",
		ExpectedEpoch: "epoch", ExpectedGeneration: 99, RequestID: "stale-reload",
	})
	if response.Result == nil || response.Result.Failure == nil ||
		response.Result.Failure.Code != "stale_generation" {
		t.Fatalf("reload response = %#v", response)
	}
	status := runtime.status()
	if !status.PersistentDrift || status.CanonicalDigest == status.DesiredDigest {
		t.Fatalf("stale reload did not expose canonical drift: %#v", status)
	}
}

func TestRuntimeManagementValidatesMutationEnvelopeBeforeOpeningPath(t *testing.T) {
	runtime := testRuntimeManagement(t, &fakeEndpointRefresher{})
	runtime.reconcileActive = true
	opened := false
	runtime.loadConfig = func(string) (*config.Config, error) {
		opened = true
		return nil, errors.New("unexpected open")
	}
	response := runtime.handle(context.Background(), management.Request{
		Operation: management.OperationReconcile, Interface: "wg0",
		CandidatePath: "/protected/candidate.conf",
	})
	if response.Failure == nil || response.Failure.Code != "validation_failed" || opened {
		t.Fatalf("invalid request response = %#v, opened = %t", response, opened)
	}
}
