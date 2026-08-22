package quick

import (
	"context"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

type endpointRefresher interface {
	RefreshAll(context.Context) error
	RefreshPeer(context.Context, string) error
}

type cleanupReporter interface {
	CleanupPending() int
	CleanupPendingFor(string, string) bool
}

type endpointStatusProvider interface {
	Status() []endpoint.Status
}

type runtimeManagement struct {
	mu              sync.RWMutex
	name            string
	coordinator     *reconcile.Coordinator
	refresher       endpointRefresher
	cleanup         cleanupReporter
	endpointStatus  endpointStatusProvider
	core            control.Client
	canonicalPath   string
	canonicalDigest string
	canonicalError  string
	recovery        management.RecoveryStatus
	reconcileActive bool
	loadConfig      func(string) (*config.Config, error)
	server          *management.Server
}

type runtimeManagementOptions struct {
	CanonicalPath   string
	Executor        reconcile.Executor
	ReconcileActive bool
	LoadConfig      func(string) (*config.Config, error)
	Core            control.Client
	Recovery        management.RecoveryStatus
}

func startRuntimeManagement(
	ctx context.Context,
	path, name string,
	initial *reconcile.Desired,
	refresher endpointRefresher,
	options runtimeManagementOptions,
) (*runtimeManagement, error) {
	executor := options.Executor
	if executor == nil {
		executor = reconcile.ExecuteFunc(func(context.Context, reconcile.Transaction) reconcile.ApplyResult {
			return reconcile.ApplyResult{
				State: reconcile.StateRolledBack,
				Failure: &reconcile.Failure{
					Code:    "unsupported_capability",
					Stage:   reconcile.StateValidating,
					Message: "runtime peer mutation primitives are not available",
				},
			}
		})
	}
	coordinator, err := reconcile.NewCoordinator(
		ctx,
		initial,
		executor,
		reconcile.Options{},
	)
	if err != nil {
		return nil, err
	}
	runtime := &runtimeManagement{
		name: name, coordinator: coordinator, refresher: refresher,
		canonicalPath: options.CanonicalPath, canonicalDigest: initial.Digest(),
		reconcileActive: options.ReconcileActive, loadConfig: options.LoadConfig,
		core: options.Core, recovery: options.Recovery,
	}
	if runtime.recovery.State == "" {
		runtime.recovery.State = "clean"
	}
	if status, ok := refresher.(endpointStatusProvider); ok {
		runtime.endpointStatus = status
	}
	if cleanup, ok := executor.(cleanupReporter); ok {
		runtime.cleanup = cleanup
	}
	if runtime.loadConfig == nil {
		runtime.loadConfig = openSecureConfigSnapshot
	}
	server, err := management.Start(ctx, path, management.HandlerFunc(runtime.handle))
	if err != nil {
		coordinator.Close()
		return nil, err
	}
	runtime.server = server
	return runtime, nil
}

func (r *runtimeManagement) handle(ctx context.Context, request management.Request) management.Response {
	if request.Interface != r.name {
		return management.ErrorResponse(
			"validation_failed", "management request names a different interface", false,
		)
	}
	capabilities := r.capabilities()
	available := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		available[capability] = struct{}{}
	}
	for _, capability := range request.RequiredCapabilities {
		if _, exists := available[capability]; !exists {
			return management.ErrorResponse(
				"unsupported_capability",
				"required capability "+capability+" is not available",
				false,
			)
		}
	}
	switch request.Operation {
	case management.OperationStatus:
		return management.Response{Status: r.status()}
	case management.OperationTransactionStatus:
		result, exists := r.coordinator.TransactionStatus(request.RequestID)
		if !exists {
			return management.ErrorResponse(
				"unknown_request_id", "transaction request ID is not retained", false,
			)
		}
		r.reconcileCleanupResult(&result)
		return management.Response{Result: &result}
	case management.OperationRefreshEndpoints:
		if r.refresher == nil {
			return management.ErrorResponse(
				"unsupported_capability", "endpoint refresh is not available", false,
			)
		}
		var err error
		if request.PublicKey == "" {
			err = r.refresher.RefreshAll(ctx)
		} else {
			err = r.refresher.RefreshPeer(ctx, request.PublicKey)
		}
		if err != nil {
			return management.Response{
				Failure: &reconcile.Failure{
					Code: "endpoint_resolution_failed", Stage: reconcile.StateSwitching,
					Peer: request.PublicKey, Retryable: true, Message: err.Error(),
				},
			}
		}
		return management.Response{OperationResult: &management.OperationResult{
			Operation: request.Operation, Interface: r.name, Peer: request.PublicKey,
		}}
	case management.OperationReconcile, management.OperationReload:
		return r.reconcile(ctx, request)
	default:
		return management.ErrorResponse("unsupported_operation", "unsupported management operation", false)
	}
}

func (r *runtimeManagement) reconcile(
	ctx context.Context,
	request management.Request,
) management.Response {
	if !r.reconcileActive {
		return management.ErrorResponse(
			"unsupported_capability",
			"peer_reconcile_v1 is not available until core, endpoint, and route transactions are active",
			false,
		)
	}
	if request.ExpectedEpoch == "" || request.ExpectedGeneration == 0 || request.RequestID == "" {
		return management.ErrorResponse(
			"validation_failed",
			"expected_epoch, expected_generation, and request_id are required",
			false,
		)
	}
	path := request.CandidatePath
	switch request.Operation {
	case management.OperationReload:
		if path != "" {
			return management.ErrorResponse(
				"validation_failed", "reload does not accept candidate_path", false,
			)
		}
		path = r.canonicalPath
	case management.OperationReconcile:
		if path == "" {
			return management.ErrorResponse(
				"validation_failed", "candidate_path is required for reconcile", false,
			)
		}
	}
	if path == "" {
		return management.ErrorResponse(
			"validation_failed", "canonical configuration path is unavailable", false,
		)
	}
	cfg, err := r.loadConfig(path)
	if err != nil {
		if request.Operation == management.OperationReload {
			r.setCanonicalError(err)
		}
		return management.ErrorResponse("validation_failed", err.Error(), false)
	}
	desired, err := reconcile.FromConfig(cfg, r.coordinator.Current())
	if err != nil {
		return management.ErrorResponse("validation_failed", err.Error(), false)
	}
	if request.Operation == management.OperationReload {
		r.mu.Lock()
		r.canonicalDigest = desired.Digest()
		r.canonicalError = ""
		r.mu.Unlock()
	}
	result, err := r.coordinator.Submit(ctx, reconcile.Request{
		ExpectedEpoch: request.ExpectedEpoch, ExpectedGeneration: request.ExpectedGeneration,
		RequestID: request.RequestID, Deadline: request.Deadline(), Desired: desired,
	})
	if err != nil {
		return management.ErrorResponse("validation_failed", err.Error(), false)
	}
	return management.Response{Result: &result}
}

func (r *runtimeManagement) status() *management.Status {
	r.refreshCanonicalProjection()
	coordinator := r.coordinator.Status()
	if coordinator.Transaction != nil {
		r.reconcileCleanupResult(coordinator.Transaction)
	}
	if coordinator.LastTransaction != nil {
		r.reconcileCleanupResult(coordinator.LastTransaction)
	}
	r.mu.RLock()
	canonicalDigest := r.canonicalDigest
	canonicalError := r.canonicalError
	reconcileActive := r.reconcileActive
	r.mu.RUnlock()
	capabilities := r.capabilitiesForState(reconcileActive)
	cleanupPending := 0
	if r.cleanup != nil {
		cleanupPending = r.cleanup.CleanupPending()
	}
	var coreStatus *control.Status
	if r.core != nil {
		if status, err := r.core.Status(); err == nil {
			coreStatus = &status
		}
	}
	peers := r.peerStatus(coreStatus)
	state, carrier, fecMode, obfsMode := "", "", "", ""
	var listenPort uint16
	var addresses []string
	var stats telemetry.Stats
	if coreStatus != nil {
		state, listenPort = coreStatus.State, coreStatus.ListenPort
		carrier, fecMode, obfsMode = coreStatus.Carrier, coreStatus.FECMode, coreStatus.ObfsMode
		addresses, stats = coreStatus.Addresses, coreStatus.Stats
	}
	return &management.Status{
		ProtocolVersion:   management.ProtocolVersion,
		Interface:         r.name,
		State:             state,
		ListenPort:        listenPort,
		Carrier:           carrier,
		FECMode:           fecMode,
		ObfsMode:          obfsMode,
		Addresses:         addresses,
		Stats:             stats,
		SupervisorEpoch:   coordinator.Epoch,
		DesiredGeneration: coordinator.Generation,
		DesiredDigest:     coordinator.DesiredDigest,
		CanonicalDigest:   canonicalDigest,
		CanonicalError:    canonicalError,
		PersistentDrift:   canonicalError != "" || coordinator.DesiredDigest != canonicalDigest,
		CleanupPending:    cleanupPending,
		Recovery:          r.recovery,
		Capabilities:      capabilities,
		Transaction:       coordinator.Transaction,
		LastTransaction:   coordinator.LastTransaction,
		Peers:             peers,
	}
}

func (r *runtimeManagement) refreshCanonicalProjection() {
	r.mu.RLock()
	path, loadConfig := r.canonicalPath, r.loadConfig
	r.mu.RUnlock()
	if path == "" || loadConfig == nil {
		return
	}
	cfg, err := loadConfig(path)
	if err == nil {
		var desired *reconcile.Desired
		desired, err = reconcile.FromConfig(cfg, r.coordinator.Current())
		if err == nil {
			r.mu.Lock()
			r.canonicalDigest = desired.Digest()
			r.canonicalError = ""
			r.mu.Unlock()
			return
		}
	}
	r.setCanonicalError(err)
}

func (r *runtimeManagement) setCanonicalError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.canonicalError = err.Error()
	r.mu.Unlock()
}

func (r *runtimeManagement) capabilities() []string {
	r.mu.RLock()
	reconcileActive := r.reconcileActive
	r.mu.RUnlock()
	return r.capabilitiesForState(reconcileActive)
}

func (r *runtimeManagement) capabilitiesForState(reconcileActive bool) []string {
	capabilities := []string{"management_protocol_v1"}
	if r.refresher != nil {
		capabilities = append(capabilities, "endpoint_refresh_v1")
	}
	if reconcileActive {
		capabilities = append(capabilities,
			"peer_reconcile_v1",
			"typed_peer_transactions_v1",
			"dynamic_obfs_keys",
			"authenticated_endpoint_generation",
			"dynamic_peer_fec_policy",
		)
	}
	return capabilities
}

func (r *runtimeManagement) reconcileCleanupResult(result *reconcile.Result) {
	if result == nil || !result.CleanupPending || r.cleanup == nil ||
		r.cleanup.CleanupPendingFor(result.Epoch, result.RequestID) {
		return
	}
	result.CleanupPending = false
	if result.Failure != nil && result.Failure.Code == "cleanup_pending" {
		result.Failure = nil
	}
}

func (r *runtimeManagement) peerStatus(coreStatus *control.Status) []management.PeerStatus {
	if r.endpointStatus == nil {
		return nil
	}
	endpointStatuses := r.endpointStatus.Status()
	result := make([]management.PeerStatus, 0, len(endpointStatuses))
	for _, item := range endpointStatuses {
		result = append(result, management.PeerStatus{
			PublicKey: item.PublicKey, ConfiguredEndpoint: item.ConfiguredEndpoint,
			SelectedEndpoint: item.SelectedEndpoint, DNSCandidates: item.DNSCandidates,
			LastResolvedAt:      optionalTime(item.LastResolvedAt),
			NextRefreshAt:       optionalTime(item.NextRefreshAt),
			LastResolutionError: item.LastResolutionError,
			EndpointGeneration:  item.Generation,
		})
	}
	if coreStatus == nil {
		return result
	}
	byKey := make(map[string]control.PeerStatus, len(coreStatus.Peers))
	for _, peer := range coreStatus.Peers {
		byKey[peer.PublicKey] = peer
	}
	for index := range result {
		peer, exists := byKey[result[index].PublicKey]
		if !exists {
			continue
		}
		result[index].Session = peer.Session
		result[index].Endpoint = peer.Endpoint
		result[index].CurrentEndpoint = peer.Endpoint
		result[index].EndpointGeneration = peer.Generation
		result[index].AuthenticatedGeneration = peer.AuthenticatedGeneration
		result[index].AuthenticatedEndpoint = peer.AuthenticatedEndpoint
		result[index].LatestHandshake = peer.LatestHandshake
		result[index].LastRx = peer.LastRx
		result[index].LastTx = peer.LastTx
		result[index].LastActivity = peer.LastActivity
		result[index].LastDirection = peer.LastDirection
		result[index].ReconnectAttempts = peer.ReconnectAttempts
		result[index].ReconnectFailures = peer.ReconnectFailures
		result[index].ConsecutiveReconnectFailures = peer.ConsecutiveReconnectFailures
		result[index].TransferRx = peer.TransferRx
		result[index].TransferTx = peer.TransferTx
		result[index].FECPolicy = peer.FECPolicy
	}
	return result
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func (r *runtimeManagement) Close() error {
	if r == nil {
		return nil
	}
	r.coordinator.Close()
	if r.server == nil {
		return nil
	}
	return r.server.Close()
}
