package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func coordinatorDesired(t *testing.T, endpoint string, keepalive uint16) *Desired {
	t.Helper()
	return mustDesired(t, modelTestConfig(config.Peer{
		PublicKey: modelTestKey(1), Endpoint: endpoint,
		PersistentKeepalive: keepalive,
		AllowedIPs:          []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
	}), nil)
}

func newTestCoordinator(
	t *testing.T,
	initial *Desired,
	executor Executor,
	modify func(*Options),
) *Coordinator {
	t.Helper()
	options := Options{
		Epoch:               "test-epoch",
		FingerprintKey:      []byte("0123456789abcdef0123456789abcdef"),
		MinOperationTimeout: 10 * time.Millisecond,
		MaxOperationTimeout: time.Minute,
	}
	if modify != nil {
		modify(&options)
	}
	coordinator, err := NewCoordinator(context.Background(), initial, executor, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coordinator.Close)
	return coordinator
}

func TestCoordinatorCommitsOnceAndReturnsCachedResultBeforeCAS(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	desired := coordinatorDesired(t, "new.example:443", 25)
	var calls atomic.Int32
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(_ context.Context, transaction Transaction) ApplyResult {
		calls.Add(1)
		if transaction.BaseGeneration != 1 || transaction.NextGeneration != 2 {
			t.Fatalf("transaction generations = %d -> %d", transaction.BaseGeneration, transaction.NextGeneration)
		}
		transaction.SetState(StateSwitching)
		transaction.SetState(StateCommitting)
		return ApplyResult{State: StateCommitted}
	}), nil)
	request := Request{
		ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
		RequestID: "request-1", Desired: desired,
		Deadline: time.Now().Add(time.Minute),
	}
	first, err := coordinator.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != StateCommitted || first.Generation != 2 || !first.Changed {
		t.Fatalf("first result = %#v", first)
	}
	if len(first.Updated) != 1 || first.Updated[0] != modelTestKey(1) {
		t.Fatalf("updated peers = %#v", first.Updated)
	}

	// This still carries generation 1. Request-ID lookup must happen before
	// the stale-generation check and return the original result.
	retry := request
	retry.Deadline = time.Now().Add(2 * time.Minute)
	second, err := coordinator.Submit(context.Background(), retry)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != StateCommitted || second.Generation != 2 || calls.Load() != 1 {
		t.Fatalf("cached result = %#v, calls = %d", second, calls.Load())
	}
	status := coordinator.Status()
	if status.Generation != 2 || status.DesiredDigest != desired.Digest() {
		t.Fatalf("coordinator status = %#v", status)
	}
}

func TestCoordinatorRejectsRequestIDCollision(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(context.Context, Transaction) ApplyResult {
		return ApplyResult{State: StateCommitted}
	}), nil)
	first := Request{
		ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
		RequestID: "same-id", Desired: coordinatorDesired(t, "new.example:443", 0),
	}
	if result, err := coordinator.Submit(context.Background(), first); err != nil || result.State != StateCommitted {
		t.Fatalf("first request = %#v, %v", result, err)
	}
	second := first
	second.Desired = coordinatorDesired(t, "different.example:443", 0)
	result, err := coordinator.Submit(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateRejected || result.Failure == nil || result.Failure.Code != "request_id_conflict" {
		t.Fatalf("collision result = %#v", result)
	}
}

func TestCoordinatorCachesStaleAndNoOpResults(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	var calls atomic.Int32
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(context.Context, Transaction) ApplyResult {
		calls.Add(1)
		return ApplyResult{State: StateCommitted}
	}), nil)

	stale, err := coordinator.Submit(context.Background(), Request{
		ExpectedEpoch: "wrong", ExpectedGeneration: 1,
		RequestID: "stale", Desired: initial,
	})
	if err != nil || stale.Failure == nil || stale.Failure.Code != "stale_epoch" {
		t.Fatalf("stale result = %#v, %v", stale, err)
	}
	staleAgain, err := coordinator.Submit(context.Background(), Request{
		ExpectedEpoch: "wrong", ExpectedGeneration: 1,
		RequestID: "stale", Desired: initial,
	})
	if err != nil || staleAgain.Failure == nil || staleAgain.Failure.Code != "stale_epoch" {
		t.Fatalf("cached stale result = %#v, %v", staleAgain, err)
	}

	noOp, err := coordinator.Submit(context.Background(), Request{
		ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
		RequestID: "noop", Desired: initial,
	})
	if err != nil || noOp.State != StateNoOp || noOp.Generation != 1 || noOp.Changed {
		t.Fatalf("no-op result = %#v, %v", noOp, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("executor calls = %d, want 0", calls.Load())
	}
}

func TestCoordinatorClientDisconnectDoesNotCancelAcceptedTransaction(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	desired := coordinatorDesired(t, "new.example:443", 0)
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(ctx context.Context, transaction Transaction) ApplyResult {
		transaction.SetState(StateSwitching)
		close(started)
		select {
		case <-release:
			return ApplyResult{State: StateCommitted}
		case <-ctx.Done():
			return ApplyResult{State: StateRolledBack, Failure: &Failure{Code: "cancelled"}}
		}
	}), nil)

	waitCtx, cancelWait := context.WithCancel(context.Background())
	resultChannel := make(chan Result, 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, err := coordinator.Submit(waitCtx, Request{
			ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
			RequestID: "disconnect", Desired: desired,
		})
		resultChannel <- result
		errorChannel <- err
	}()
	<-started
	cancelWait()
	waitResult := <-resultChannel
	waitErr := <-errorChannel
	if !errors.Is(waitErr, context.Canceled) || waitResult.State != StateSwitching {
		t.Fatalf("cancelled wait = %#v, %v", waitResult, waitErr)
	}
	if status := coordinator.Status(); status.Transaction == nil || status.Transaction.State != StateSwitching {
		t.Fatalf("in-progress status = %#v", status)
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for {
		result, exists := coordinator.TransactionStatus("disconnect")
		if exists && result.State == StateCommitted {
			if result.Generation != 2 {
				t.Fatalf("recovered generation = %d", result.Generation)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("transaction did not finish: %#v, exists=%t", result, exists)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCoordinatorSerializesDesiredTransactions(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(context.Context, Transaction) ApplyResult {
		once.Do(func() { close(started) })
		<-release
		return ApplyResult{State: StateCommitted}
	}), nil)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = coordinator.Submit(context.Background(), Request{
			ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
			RequestID: "first", Desired: coordinatorDesired(t, "first.example:443", 0),
		})
	}()
	<-started
	second, err := coordinator.Submit(context.Background(), Request{
		ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
		RequestID: "second", Desired: coordinatorDesired(t, "second.example:443", 0),
	})
	if err != nil || second.Failure == nil || second.Failure.Code != "transaction_in_progress" {
		t.Fatalf("second transaction = %#v, %v", second, err)
	}
	close(release)
	<-firstDone
}

func TestCoordinatorCachesTerminalFailure(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	var calls atomic.Int32
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(context.Context, Transaction) ApplyResult {
		calls.Add(1)
		return ApplyResult{
			State:   StateRolledBack,
			Failure: &Failure{Code: "readiness_timeout", Stage: StateSwitching, Retryable: true, Message: "not ready"},
		}
	}), nil)
	request := Request{
		ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
		RequestID: "failed", Desired: coordinatorDesired(t, "new.example:443", 0),
	}
	for range 2 {
		result, err := coordinator.Submit(context.Background(), request)
		if err != nil || result.State != StateRolledBack || result.Failure == nil || result.Failure.Code != "readiness_timeout" {
			t.Fatalf("failure result = %#v, %v", result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("executor calls = %d, want 1", calls.Load())
	}
}

func TestCoordinatorExpiryMakesOrdinaryRetryPassThroughCAS(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	desired := coordinatorDesired(t, "new.example:443", 0)
	now := time.Now()
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(context.Context, Transaction) ApplyResult {
		return ApplyResult{State: StateCommitted}
	}), func(options *Options) {
		options.CacheTTL = time.Second
		options.Now = func() time.Time { return now }
	})
	request := Request{
		ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
		RequestID: "expires", Desired: desired,
	}
	first, err := coordinator.Submit(context.Background(), request)
	if err != nil || first.State != StateCommitted {
		t.Fatalf("first result = %#v, %v", first, err)
	}
	now = now.Add(2 * time.Second)
	if _, exists := coordinator.TransactionStatus("expires"); exists {
		t.Fatal("expired transaction status still exists")
	}
	second, err := coordinator.Submit(context.Background(), request)
	if err != nil || second.Failure == nil || second.Failure.Code != "stale_generation" {
		t.Fatalf("expired retry result = %#v, %v", second, err)
	}
}

func TestCoordinatorRejectsRestartRequiredBeforeExecutor(t *testing.T) {
	initial := coordinatorDesired(t, "old.example:443", 0)
	candidate := initial.Config()
	candidate.Interface.PostUp = []string{"echo changed"}
	desired := mustDesired(t, candidate, initial)
	var calls atomic.Int32
	coordinator := newTestCoordinator(t, initial, ExecuteFunc(func(context.Context, Transaction) ApplyResult {
		calls.Add(1)
		return ApplyResult{State: StateCommitted}
	}), nil)
	result, err := coordinator.Submit(context.Background(), Request{
		ExpectedEpoch: "test-epoch", ExpectedGeneration: 1,
		RequestID: "restart", Desired: desired,
	})
	if err != nil || result.Failure == nil || result.Failure.Code != "restart_required" || !result.RestartRequired {
		t.Fatalf("restart result = %#v, %v", result, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("executor calls = %d, want 0", calls.Load())
	}
}
