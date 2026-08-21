package quick

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

type executorRecorder struct {
	calls []string
	fail  map[string]error
}

func (r *executorRecorder) call(name string) error {
	r.calls = append(r.calls, name)
	return r.fail[name]
}

type fakePeerSetClient struct {
	recorder *executorRecorder
	request  control.PeerSetRequest
}

func (*fakePeerSetClient) Status() (control.Status, error)                      { return control.Status{}, nil }
func (*fakePeerSetClient) SetPeerEndpoint(control.SetPeerEndpointRequest) error { return nil }
func (*fakePeerSetClient) RedialPeer(string) error                              { return nil }
func (*fakePeerSetClient) Activate() error                                      { return nil }
func (*fakePeerSetClient) FinalizePeerEndpoint(string, uint64) error            { return nil }
func (c *fakePeerSetClient) PreparePeerSet(request control.PeerSetRequest) error {
	c.request = request
	return c.recorder.call("core.prepare")
}
func (c *fakePeerSetClient) CommitPeerSet(string) error {
	return c.recorder.call("core.commit")
}
func (c *fakePeerSetClient) RollbackPeerSet(string) error {
	return c.recorder.call("core.rollback")
}
func (c *fakePeerSetClient) FinalizePeerSet(string) error {
	return c.recorder.call("core.finalize")
}

type fakePeerRouteManager struct{ recorder *executorRecorder }

func (m *fakePeerRouteManager) Prepare(
	context.Context,
	platform.PeerRoutePlan,
) (platform.PreparedPeerRoutes, error) {
	if err := m.recorder.call("routes.prepare"); err != nil {
		return nil, err
	}
	return &fakePreparedPeerRoutes{recorder: m.recorder}, nil
}

type fakePreparedPeerRoutes struct{ recorder *executorRecorder }

func (p *fakePreparedPeerRoutes) CommitRemovals(context.Context) error {
	return p.recorder.call("routes.commit_removals")
}
func (p *fakePreparedPeerRoutes) CommitAdditions(context.Context) error {
	return p.recorder.call("routes.commit_additions")
}
func (p *fakePreparedPeerRoutes) RollbackAdditions(context.Context) error {
	return p.recorder.call("routes.rollback_additions")
}
func (p *fakePreparedPeerRoutes) RollbackRemovals(context.Context) error {
	return p.recorder.call("routes.rollback_removals")
}
func (p *fakePreparedPeerRoutes) Rollback(ctx context.Context) error {
	if err := p.RollbackAdditions(ctx); err != nil {
		return err
	}
	return p.RollbackRemovals(ctx)
}
func (p *fakePreparedPeerRoutes) Finalize(context.Context) error {
	return p.recorder.call("routes.finalize")
}

type fakePeerEndpointManager struct{ recorder *executorRecorder }

func (m *fakePeerEndpointManager) PreparePeerEndpoints(
	context.Context,
	string,
	*reconcile.Desired,
	*reconcile.Desired,
	reconcile.Diff,
) (preparedPeerEndpoints, error) {
	err := m.recorder.call("endpoints.prepare")
	return &fakePreparedPeerEndpoints{recorder: m.recorder}, err
}

type fakePreparedPeerEndpoints struct{ recorder *executorRecorder }

func (p *fakePreparedPeerEndpoints) Commit(context.Context) error {
	return p.recorder.call("endpoints.commit")
}
func (p *fakePreparedPeerEndpoints) Rollback(context.Context) error {
	return p.recorder.call("endpoints.rollback")
}
func (p *fakePreparedPeerEndpoints) Finalize(context.Context) error {
	return p.recorder.call("endpoints.finalize")
}

func executorDesired(t *testing.T, peers ...config.Peer) *reconcile.Desired {
	t.Helper()
	desired, err := reconcile.FromConfig(&config.Config{
		Interface: config.Interface{PrivateKey: testQuickKey(90)},
		Peers:     peers,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return desired
}

func executorTransaction(t *testing.T) reconcile.Transaction {
	t.Helper()
	current := executorDesired(t,
		config.Peer{
			PublicKey: testQuickKey(1), Endpoint: "old.example:443",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
		},
		config.Peer{
			PublicKey:  testQuickKey(3),
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.3.0.0/16")},
		},
	)
	desired := executorDesired(t,
		config.Peer{
			PublicKey: testQuickKey(1), Endpoint: "new.example:443", PersistentKeepalive: 25,
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
		},
		config.Peer{
			PublicKey:  testQuickKey(2),
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")},
		},
	)
	return reconcile.Transaction{
		Epoch: "epoch", RequestID: "request", BaseGeneration: 1, NextGeneration: 2,
		Current: current, Desired: desired, Diff: reconcile.Compare(current, desired),
	}
}

func newFakeReconcileExecutor(t *testing.T, recorder *executorRecorder) (*reconcileExecutor, *fakePeerSetClient) {
	t.Helper()
	core := &fakePeerSetClient{recorder: recorder}
	executor, err := newReconcileExecutor(
		context.Background(),
		core,
		&fakePeerRouteManager{recorder: recorder},
		&fakePeerEndpointManager{recorder: recorder},
	)
	if err != nil {
		t.Fatal(err)
	}
	return executor, core
}

func TestReconcileExecutorUsesSafeCommitAndFinalizeOrder(t *testing.T) {
	recorder := &executorRecorder{fail: make(map[string]error)}
	executor, core := newFakeReconcileExecutor(t, recorder)
	result := executor.Execute(context.Background(), executorTransaction(t))
	if result.State != reconcile.StateCommitted || result.Failure != nil || result.CleanupPending {
		t.Fatalf("result = %#v", result)
	}
	want := []string{
		"routes.prepare", "core.prepare", "endpoints.prepare",
		"routes.commit_removals", "core.commit", "endpoints.commit", "routes.commit_additions",
		"endpoints.finalize", "core.finalize", "routes.finalize",
	}
	if !slices.Equal(recorder.calls, want) {
		t.Fatalf("calls = %#v, want %#v", recorder.calls, want)
	}
	if core.request.TransactionID != "epoch:request" || len(core.request.Mutations) != 3 {
		t.Fatalf("core request = %#v", core.request)
	}
	operations := []string{
		core.request.Mutations[0].Operation,
		core.request.Mutations[1].Operation,
		core.request.Mutations[2].Operation,
	}
	if !slices.Equal(operations, []string{"add", "update", "remove"}) {
		t.Fatalf("mutation operations = %#v", operations)
	}
}

func TestReconcileExecutorRollsBackInReverseSafeOrder(t *testing.T) {
	recorder := &executorRecorder{fail: map[string]error{
		"routes.commit_additions": errors.New("route add failed"),
	}}
	executor, _ := newFakeReconcileExecutor(t, recorder)
	result := executor.Execute(context.Background(), executorTransaction(t))
	if result.State != reconcile.StateRolledBack || result.Failure == nil ||
		result.Failure.Code != "commit_failed" {
		t.Fatalf("result = %#v", result)
	}
	wantTail := []string{
		"routes.rollback_additions", "endpoints.rollback", "core.rollback",
		"routes.rollback_removals",
	}
	if !slices.Equal(recorder.calls[len(recorder.calls)-len(wantTail):], wantTail) {
		t.Fatalf("rollback calls = %#v", recorder.calls)
	}
}

func TestReconcileExecutorRetainsCoreWhenNewRouteCannotBeWithdrawn(t *testing.T) {
	recorder := &executorRecorder{fail: map[string]error{
		"routes.commit_additions":   errors.New("route add failed"),
		"routes.rollback_additions": errors.New("route delete failed"),
	}}
	executor, _ := newFakeReconcileExecutor(t, recorder)
	result := executor.Execute(context.Background(), executorTransaction(t))
	if result.State != reconcile.StateDegraded || result.Failure == nil ||
		result.Failure.Code != "rollback_failed" || !result.Failure.Degraded {
		t.Fatalf("result = %#v", result)
	}
	if slices.Contains(recorder.calls, "core.rollback") ||
		slices.Contains(recorder.calls, "routes.rollback_removals") {
		t.Fatalf("unsafe rollback continued after route withdrawal failed: %#v", recorder.calls)
	}
}

func TestReconcileExecutorReportsCleanupPendingAfterCommit(t *testing.T) {
	recorder := &executorRecorder{fail: map[string]error{
		"endpoints.finalize": errors.New("lease busy"),
	}}
	executor, _ := newFakeReconcileExecutor(t, recorder)
	result := executor.Execute(context.Background(), executorTransaction(t))
	if result.State != reconcile.StateCommitted || !result.CleanupPending ||
		result.Failure == nil || result.Failure.Code != "cleanup_pending" ||
		!result.Failure.Committed {
		t.Fatalf("result = %#v", result)
	}
	if !slices.Contains(recorder.calls, "core.finalize") ||
		!slices.Contains(recorder.calls, "routes.finalize") {
		t.Fatalf("remaining finalizers were skipped: %#v", recorder.calls)
	}
	if got := executor.CleanupPending(); got != 1 {
		t.Fatalf("cleanup jobs = %d, want 1", got)
	}
	delete(recorder.fail, "endpoints.finalize")
	executor.retryCleanup(context.Background())
	if got := executor.CleanupPending(); got != 0 {
		t.Fatalf("cleanup jobs after retry = %d", got)
	}
	if got := countExecutorCalls(recorder.calls, "core.finalize"); got != 1 {
		t.Fatalf("successful core finalizer retried %d times", got)
	}
	if got := countExecutorCalls(recorder.calls, "routes.finalize"); got != 1 {
		t.Fatalf("successful route finalizer retried %d times", got)
	}
}

func countExecutorCalls(calls []string, value string) int {
	count := 0
	for _, call := range calls {
		if call == value {
			count++
		}
	}
	return count
}

func TestReconcileExecutorDoesNotRollbackUnknownCoreTransaction(t *testing.T) {
	recorder := &executorRecorder{fail: map[string]error{
		"core.prepare": errors.New("invalid peer projection"),
	}}
	executor, _ := newFakeReconcileExecutor(t, recorder)
	result := executor.Execute(context.Background(), executorTransaction(t))
	if result.State != reconcile.StateRolledBack || result.Failure == nil ||
		result.Failure.Code != "peer_prepare_failed" {
		t.Fatalf("result = %#v", result)
	}
	if slices.Contains(recorder.calls, "core.rollback") {
		t.Fatalf("unprepared core transaction was rolled back: %#v", recorder.calls)
	}
}
