package quick

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

const reconcileCleanupTimeout = 15 * time.Second

// peerEndpointTransactionManager owns tentative endpoint generations and the
// refresh-worker projection for the affected peers. Prepare may resolve and
// probe candidates, but Commit is the publication boundary.
type peerEndpointTransactionManager interface {
	PreparePeerEndpoints(
		context.Context,
		string,
		*reconcile.Desired,
		*reconcile.Desired,
		reconcile.Diff,
	) (preparedPeerEndpoints, error)
}

type preparedPeerEndpoints interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	Finalize(context.Context) error
}

type reconcileExecutor struct {
	core           control.Client
	routes         platform.PeerRouteManager
	endpoints      peerEndpointTransactionManager
	cleanupTimeout time.Duration
	cleanupRetry   time.Duration
	cleanupMu      sync.Mutex
	cleanupJobs    map[string]*reconcileCleanupJob
	cleanupWake    chan struct{}
}

type reconcileCleanupJob struct {
	transactionID string
	endpoints     preparedPeerEndpoints
	routes        platform.PreparedPeerRoutes
	endpointsDone bool
	coreDone      bool
	routesDone    bool
}

type supervisorPeerEndpointTransactions struct {
	supervisor *endpoint.Supervisor
}

func (m supervisorPeerEndpointTransactions) PreparePeerEndpoints(
	ctx context.Context,
	transactionID string,
	_ *reconcile.Desired,
	desired *reconcile.Desired,
	_ reconcile.Diff,
) (preparedPeerEndpoints, error) {
	if m.supervisor == nil {
		return nil, errors.New("endpoint supervisor is required")
	}
	cfg := desired.Config()
	if cfg == nil {
		return nil, errors.New("desired endpoint configuration is required")
	}
	peers := make([]endpoint.PeerSpec, 0, len(cfg.Peers))
	for _, peer := range cfg.Peers {
		peers = append(peers, endpoint.PeerSpec{
			PublicKey: peer.PublicKey, Endpoint: peer.Endpoint,
		})
	}
	return m.supervisor.PreparePeerSet(ctx, transactionID, endpoint.PeerSetPlan{Peers: peers})
}

func newReconcileExecutor(
	ctx context.Context,
	core control.Client,
	routes platform.PeerRouteManager,
	endpoints peerEndpointTransactionManager,
) (*reconcileExecutor, error) {
	if core == nil {
		return nil, errors.New("core peer transaction client is required")
	}
	if routes == nil {
		return nil, errors.New("peer route manager is required")
	}
	if endpoints == nil {
		return nil, errors.New("peer endpoint transaction manager is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executor := &reconcileExecutor{
		core: core, routes: routes, endpoints: endpoints,
		cleanupTimeout: reconcileCleanupTimeout, cleanupRetry: 5 * time.Second,
		cleanupJobs: make(map[string]*reconcileCleanupJob), cleanupWake: make(chan struct{}, 1),
	}
	go executor.cleanupLoop(ctx)
	return executor, nil
}

func (e *reconcileExecutor) Execute(
	ctx context.Context,
	transaction reconcile.Transaction,
) reconcile.ApplyResult {
	mutations, err := controlPeerMutations(transaction)
	if err != nil {
		return failedReconcile("validation_failed", reconcile.StatePreparing, err, false)
	}
	transactionID := transaction.Epoch + ":" + transaction.RequestID

	transaction.SetState(reconcile.StatePreparing)
	routeLease, err := e.routes.Prepare(ctx, platform.PeerRoutePlan{
		TransactionID: transactionID,
		Additions:     transaction.Diff.RouteAdditions,
		Removals:      transaction.Diff.RouteRemovals,
	})
	if err != nil {
		return failedReconcile("route_lease_failed", reconcile.StatePreparing, err, true)
	}
	if err := e.core.PreparePeerSet(control.PeerSetRequest{
		TransactionID: transactionID,
		Mutations:     mutations,
	}); err != nil {
		return e.rollbackPrepared(
			transactionID, false, nil, routeLease,
			"peer_prepare_failed", reconcile.StatePreparing, err, false,
		)
	}

	transaction.SetState(reconcile.StateSwitching)
	endpointLease, err := e.endpoints.PreparePeerEndpoints(
		ctx, transactionID, transaction.Current, transaction.Desired, transaction.Diff,
	)
	if err != nil {
		code := "endpoint_resolution_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "readiness_timeout"
		}
		return e.rollbackPrepared(
			transactionID, true, endpointLease, routeLease,
			code, reconcile.StateSwitching, err, true,
		)
	}

	transaction.SetState(reconcile.StateCommitting)
	if err := routeLease.CommitRemovals(ctx); err != nil {
		return e.rollbackPrepared(
			transactionID, true, endpointLease, routeLease,
			"commit_failed", reconcile.StateCommitting, err, true,
		)
	}
	if err := e.core.CommitPeerSet(transactionID); err != nil {
		return e.rollbackPrepared(
			transactionID, true, endpointLease, routeLease,
			"commit_failed", reconcile.StateCommitting, err, true,
		)
	}
	if err := endpointLease.Commit(ctx); err != nil {
		return e.rollbackCommitted(
			transactionID, endpointLease, routeLease,
			"commit_failed", reconcile.StateCommitting, err, true,
		)
	}
	if err := routeLease.CommitAdditions(ctx); err != nil {
		return e.rollbackCommitted(
			transactionID, endpointLease, routeLease,
			"commit_failed", reconcile.StateCommitting, err, true,
		)
	}

	transaction.SetState(reconcile.StateFinalizing)
	cleanupCtx, cancel := e.cleanupContext()
	defer cancel()
	job := &reconcileCleanupJob{
		transactionID: transactionID, endpoints: endpointLease, routes: routeLease,
	}
	if err := e.finalizeCleanupJob(cleanupCtx, job); err != nil {
		e.queueCleanup(job)
		return reconcile.ApplyResult{
			State:          reconcile.StateCommitted,
			CleanupPending: true,
			Failure: &reconcile.Failure{
				Code: "cleanup_pending", Stage: reconcile.StateFinalizing,
				Retryable: true, Committed: true, Message: err.Error(),
			},
		}
	}
	return reconcile.ApplyResult{State: reconcile.StateCommitted}
}

func (e *reconcileExecutor) finalizeCleanupJob(
	ctx context.Context,
	job *reconcileCleanupJob,
) error {
	var cleanupErrors []error
	if !job.endpointsDone {
		if err := job.endpoints.Finalize(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("finalize peer endpoints: %w", err))
		} else {
			job.endpointsDone = true
		}
	}
	if !job.coreDone {
		if err := e.core.FinalizePeerSet(job.transactionID); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("finalize core peer set: %w", err))
		} else {
			job.coreDone = true
		}
	}
	if !job.routesDone {
		if err := job.routes.Finalize(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("finalize peer routes: %w", err))
		} else {
			job.routesDone = true
		}
	}
	return errors.Join(cleanupErrors...)
}

func (e *reconcileExecutor) queueCleanup(job *reconcileCleanupJob) {
	e.cleanupMu.Lock()
	e.cleanupJobs[job.transactionID] = job
	e.cleanupMu.Unlock()
	select {
	case e.cleanupWake <- struct{}{}:
	default:
	}
}

func (e *reconcileExecutor) cleanupLoop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.cleanupWake:
			timer.Reset(e.cleanupRetry)
			continue
		case <-timer.C:
		}
		e.retryCleanup(ctx)
		e.cleanupMu.Lock()
		pending := len(e.cleanupJobs) != 0
		e.cleanupMu.Unlock()
		if pending {
			timer.Reset(e.cleanupRetry)
		}
	}
}

func (e *reconcileExecutor) retryCleanup(parent context.Context) {
	e.cleanupMu.Lock()
	ids := make([]string, 0, len(e.cleanupJobs))
	for transactionID := range e.cleanupJobs {
		ids = append(ids, transactionID)
	}
	e.cleanupMu.Unlock()
	for _, transactionID := range ids {
		e.cleanupMu.Lock()
		job := e.cleanupJobs[transactionID]
		e.cleanupMu.Unlock()
		if job == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, e.cleanupTimeout)
		err := e.finalizeCleanupJob(ctx, job)
		cancel()
		if err == nil {
			e.cleanupMu.Lock()
			if e.cleanupJobs[transactionID] == job {
				delete(e.cleanupJobs, transactionID)
			}
			e.cleanupMu.Unlock()
		}
	}
}

func (e *reconcileExecutor) CleanupPending() int {
	e.cleanupMu.Lock()
	defer e.cleanupMu.Unlock()
	return len(e.cleanupJobs)
}

func (e *reconcileExecutor) CleanupPendingFor(epoch, requestID string) bool {
	e.cleanupMu.Lock()
	defer e.cleanupMu.Unlock()
	_, exists := e.cleanupJobs[epoch+":"+requestID]
	return exists
}

func (e *reconcileExecutor) rollbackPrepared(
	transactionID string,
	corePrepared bool,
	endpointLease preparedPeerEndpoints,
	routeLease platform.PreparedPeerRoutes,
	code string,
	stage reconcile.State,
	cause error,
	retryable bool,
) reconcile.ApplyResult {
	cleanupCtx, cancel := e.cleanupContext()
	defer cancel()
	var rollbackErrors []error
	if endpointLease != nil {
		if err := endpointLease.Rollback(cleanupCtx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback peer endpoints: %w", err))
		}
	}
	if corePrepared {
		if err := e.core.RollbackPeerSet(transactionID); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback core peer set: %w", err))
		}
	}
	if err := routeLease.RollbackRemovals(cleanupCtx); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback peer route removals: %w", err))
	}
	return rollbackResult(code, stage, cause, retryable, rollbackErrors)
}

func (e *reconcileExecutor) rollbackCommitted(
	transactionID string,
	endpointLease preparedPeerEndpoints,
	routeLease platform.PreparedPeerRoutes,
	code string,
	stage reconcile.State,
	cause error,
	retryable bool,
) reconcile.ApplyResult {
	cleanupCtx, cancel := e.cleanupContext()
	defer cancel()
	if err := routeLease.RollbackAdditions(cleanupCtx); err != nil {
		return rollbackResult(code, stage, cause, retryable, []error{
			fmt.Errorf("rollback peer route additions: %w", err),
		})
	}
	var rollbackErrors []error
	if endpointLease != nil {
		if err := endpointLease.Rollback(cleanupCtx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback peer endpoints: %w", err))
		}
	}
	if err := e.core.RollbackPeerSet(transactionID); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback core peer set: %w", err))
	}
	// A route into the TUN is restored only after the old core AllowedIP
	// projection is active again.
	if len(rollbackErrors) == 0 {
		if err := routeLease.RollbackRemovals(cleanupCtx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback peer route removals: %w", err))
		}
	}
	return rollbackResult(code, stage, cause, retryable, rollbackErrors)
}

func (e *reconcileExecutor) cleanupContext() (context.Context, context.CancelFunc) {
	timeout := e.cleanupTimeout
	if timeout <= 0 {
		timeout = reconcileCleanupTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

func rollbackResult(
	code string,
	stage reconcile.State,
	cause error,
	retryable bool,
	rollbackErrors []error,
) reconcile.ApplyResult {
	if rollbackErr := errors.Join(rollbackErrors...); rollbackErr != nil {
		return reconcile.ApplyResult{
			State: reconcile.StateDegraded,
			Failure: &reconcile.Failure{
				Code: "rollback_failed", Stage: reconcile.StateRollingBack,
				Retryable: true, Degraded: true,
				Message: errors.Join(cause, rollbackErr).Error(),
			},
		}
	}
	return failedReconcile(code, stage, cause, retryable)
}

func failedReconcile(
	code string,
	stage reconcile.State,
	err error,
	retryable bool,
) reconcile.ApplyResult {
	return reconcile.ApplyResult{
		State: reconcile.StateRolledBack,
		Failure: &reconcile.Failure{
			Code: code, Stage: stage, Retryable: retryable, Message: err.Error(),
		},
	}
}

func controlPeerMutations(transaction reconcile.Transaction) ([]control.PeerMutation, error) {
	current := transaction.Current.Config()
	desired := transaction.Desired.Config()
	if current == nil || desired == nil {
		return nil, errors.New("current and desired configurations are required")
	}
	currentPeers := configPeerMap(current.Peers)
	desiredPeers := configPeerMap(desired.Peers)
	mutations := make([]control.PeerMutation, 0,
		len(transaction.Diff.Added)+len(transaction.Diff.Updated)+len(transaction.Diff.Removed),
	)
	for _, publicKey := range transaction.Diff.Added {
		peer, exists := desiredPeers[publicKey]
		if !exists {
			return nil, fmt.Errorf("added peer %s is absent from desired state", publicKey)
		}
		mutations = append(mutations, controlPeerMutation("add", peer))
	}
	for _, update := range transaction.Diff.Updated {
		peer, exists := desiredPeers[update.PublicKey]
		if !exists {
			return nil, fmt.Errorf("updated peer %s is absent from desired state", update.PublicKey)
		}
		mutations = append(mutations, controlPeerMutation("update", peer))
	}
	for _, publicKey := range transaction.Diff.Removed {
		peer, exists := currentPeers[publicKey]
		if !exists {
			return nil, fmt.Errorf("removed peer %s is absent from current state", publicKey)
		}
		mutations = append(mutations, controlPeerMutation("remove", peer))
	}
	return mutations, nil
}

func configPeerMap(peers []config.Peer) map[string]config.Peer {
	result := make(map[string]config.Peer, len(peers))
	for _, peer := range peers {
		result[peer.PublicKey] = peer
	}
	return result
}

func controlPeerMutation(operation string, peer config.Peer) control.PeerMutation {
	allowedIPs := make([]string, len(peer.AllowedIPs))
	for index, prefix := range peer.AllowedIPs {
		allowedIPs[index] = prefix.String()
	}
	return control.PeerMutation{
		Operation: operation, PublicKey: peer.PublicKey,
		PresharedKey: peer.PresharedKey, AllowedIPs: allowedIPs,
		Endpoint: peer.Endpoint, PersistentKeepalive: peer.PersistentKeepalive,
		FECPolicy: peer.FECPolicy,
	}
}
