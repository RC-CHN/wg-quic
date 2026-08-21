package reconcile

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	StateAccepted    State = "accepted"
	StateValidating  State = "validating"
	StatePreparing   State = "preparing"
	StateSwitching   State = "switching"
	StateCommitting  State = "committing"
	StateFinalizing  State = "finalizing"
	StateRollingBack State = "rolling_back"
	StateCommitted   State = "committed"
	StateNoOp        State = "no_op"
	StateRejected    State = "rejected"
	StateRolledBack  State = "rolled_back"
	StateDegraded    State = "degraded"
)

type State string

type Failure struct {
	Code      string `json:"code"`
	Stage     State  `json:"stage,omitempty"`
	Peer      string `json:"peer,omitempty"`
	Retryable bool   `json:"retryable"`
	Committed bool   `json:"committed"`
	Degraded  bool   `json:"degraded"`
	Message   string `json:"message"`
}

type Result struct {
	Epoch           string     `json:"epoch"`
	RequestID       string     `json:"request_id"`
	State           State      `json:"state"`
	Generation      uint64     `json:"generation"`
	Changed         bool       `json:"changed"`
	Added           []string   `json:"added,omitempty"`
	Updated         []string   `json:"updated,omitempty"`
	Removed         []string   `json:"removed,omitempty"`
	RestartRequired bool       `json:"restart_required"`
	CleanupPending  bool       `json:"cleanup_pending"`
	Failure         *Failure   `json:"error,omitempty"`
	RestartReasons  []string   `json:"restart_reasons,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

type Request struct {
	ExpectedEpoch      string
	ExpectedGeneration uint64
	RequestID          string
	Deadline           time.Time
	Desired            *Desired
}

type Transaction struct {
	Epoch          string
	RequestID      string
	BaseGeneration uint64
	NextGeneration uint64
	Current        *Desired
	Desired        *Desired
	Diff           Diff
	progress       func(State)
}

func (t Transaction) SetState(state State) {
	if t.progress != nil {
		t.progress(state)
	}
}

type ApplyResult struct {
	State          State
	CleanupPending bool
	Failure        *Failure
}

type Executor interface {
	Execute(context.Context, Transaction) ApplyResult
}

type ExecuteFunc func(context.Context, Transaction) ApplyResult

func (f ExecuteFunc) Execute(ctx context.Context, transaction Transaction) ApplyResult {
	return f(ctx, transaction)
}

type Options struct {
	Epoch               string
	FingerprintKey      []byte
	CacheEntries        int
	CacheTTL            time.Duration
	MinOperationTimeout time.Duration
	MaxOperationTimeout time.Duration
	Now                 func() time.Time
}

type CoordinatorStatus struct {
	Epoch           string  `json:"supervisor_epoch"`
	Generation      uint64  `json:"desired_generation"`
	DesiredDigest   string  `json:"desired_digest"`
	Transaction     *Result `json:"transaction,omitempty"`
	LastTransaction *Result `json:"last_transaction,omitempty"`
}

type Coordinator struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	executor    Executor
	options     Options
	epoch       string
	fingerprint []byte
	generation  uint64
	current     *Desired
	entries     map[string]*requestEntry
	active      string
	last        *Result
}

type requestEntry struct {
	fingerprint [sha256.Size]byte
	createdAt   time.Time
	completedAt time.Time
	state       State
	result      Result
	done        chan struct{}
}

func NewCoordinator(
	ctx context.Context,
	initial *Desired,
	executor Executor,
	options Options,
) (*Coordinator, error) {
	if initial == nil {
		return nil, errors.New("initial desired configuration is required")
	}
	if executor == nil {
		return nil, errors.New("reconciliation executor is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	options = defaultCoordinatorOptions(options)
	epoch := options.Epoch
	if epoch == "" {
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			return nil, fmt.Errorf("generate supervisor epoch: %w", err)
		}
		epoch = hex.EncodeToString(value[:])
	}
	fingerprintKey := append([]byte(nil), options.FingerprintKey...)
	if len(fingerprintKey) == 0 {
		fingerprintKey = make([]byte, sha256.Size)
		if _, err := rand.Read(fingerprintKey); err != nil {
			return nil, fmt.Errorf("generate request fingerprint key: %w", err)
		}
	}
	if len(fingerprintKey) < 16 {
		return nil, errors.New("request fingerprint key must contain at least 16 bytes")
	}
	runCtx, cancel := context.WithCancel(ctx)
	return &Coordinator{
		ctx: runCtx, cancel: cancel, executor: executor, options: options,
		epoch: epoch, fingerprint: fingerprintKey, generation: 1,
		current: initial.clone(), entries: make(map[string]*requestEntry),
	}, nil
}

func defaultCoordinatorOptions(options Options) Options {
	if options.CacheEntries <= 0 {
		options.CacheEntries = 256
	}
	if options.CacheTTL <= 0 {
		options.CacheTTL = 30 * time.Minute
	}
	if options.MinOperationTimeout <= 0 {
		options.MinOperationTimeout = time.Second
	}
	if options.MaxOperationTimeout < options.MinOperationTimeout {
		options.MaxOperationTimeout = 2 * time.Minute
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return options
}

func (c *Coordinator) Close() {
	c.cancel()
}

// Submit registers the transaction before executing it. Waiting is bound to
// ctx, but execution is bound to the coordinator and its clamped operation
// deadline, so a disconnected client never cancels an accepted mutation.
func (c *Coordinator) Submit(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRequestID(request.RequestID); err != nil {
		return Result{}, err
	}
	if request.Desired == nil {
		return Result{}, errors.New("desired configuration is required")
	}
	fingerprint, err := c.requestFingerprint(request)
	if err != nil {
		return Result{}, err
	}
	now := c.options.Now()

	c.mu.Lock()
	c.evictLocked(now)
	if existing, exists := c.entries[request.RequestID]; exists {
		if !hmac.Equal(existing.fingerprint[:], fingerprint[:]) {
			result := c.rejectedResultLocked(request.RequestID, "request_id_conflict", "request ID was already used for different content", false)
			c.mu.Unlock()
			return result, nil
		}
		c.mu.Unlock()
		return c.waitForEntry(ctx, existing)
	}

	entry := &requestEntry{
		fingerprint: fingerprint, createdAt: now, state: StateValidating,
		done: make(chan struct{}),
	}
	entry.result = Result{
		Epoch: c.epoch, RequestID: request.RequestID, State: StateValidating,
		Generation: c.generation,
	}
	c.entries[request.RequestID] = entry

	if request.ExpectedEpoch != c.epoch {
		c.finishRejectedLocked(entry, now, "stale_epoch", "supervisor epoch does not match", false, nil)
		c.mu.Unlock()
		return cloneResult(entry.result), nil
	}
	if request.ExpectedGeneration != c.generation {
		c.finishRejectedLocked(entry, now, "stale_generation", "desired generation does not match", false, nil)
		c.mu.Unlock()
		return cloneResult(entry.result), nil
	}
	if c.active != "" {
		c.finishRejectedLocked(entry, now, "transaction_in_progress", "another desired-state transaction is active", true, nil)
		c.mu.Unlock()
		return cloneResult(entry.result), nil
	}

	diff := Compare(c.current, request.Desired)
	entry.result.Added = append([]string(nil), diff.Added...)
	for _, update := range diff.Updated {
		entry.result.Updated = append(entry.result.Updated, update.PublicKey)
	}
	entry.result.Removed = append([]string(nil), diff.Removed...)
	entry.result.Changed = diff.Changed()
	if diff.RestartRequired() {
		entry.result.RestartRequired = true
		entry.result.RestartReasons = append([]string(nil), diff.RestartReasons...)
		c.finishRejectedLocked(entry, now, "restart_required", "configuration contains changes that require an interface restart", false, diff.RestartReasons)
		c.mu.Unlock()
		return cloneResult(entry.result), nil
	}
	if !diff.Changed() {
		c.finishLocked(entry, Result{
			Epoch: c.epoch, RequestID: request.RequestID, State: StateNoOp,
			Generation: c.generation, Changed: false,
		}, now)
		c.mu.Unlock()
		return cloneResult(entry.result), nil
	}

	entry.state = StateAccepted
	entry.result.State = StateAccepted
	c.active = request.RequestID
	transaction := Transaction{
		Epoch: c.epoch, RequestID: request.RequestID,
		BaseGeneration: c.generation, NextGeneration: c.generation + 1,
		Current: c.current.clone(), Desired: request.Desired.clone(), Diff: diff,
	}
	transaction.progress = func(state State) { c.setProgress(entry, state) }
	deadline := c.operationDeadline(now, request.Deadline)
	c.mu.Unlock()

	go c.execute(entry, transaction, deadline)
	return c.waitForEntry(ctx, entry)
}

func (c *Coordinator) TransactionStatus(requestID string) (Result, bool) {
	now := c.options.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(now)
	entry, exists := c.entries[requestID]
	if !exists {
		return Result{}, false
	}
	return cloneResult(entry.result), true
}

func (c *Coordinator) Status() CoordinatorStatus {
	now := c.options.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(now)
	status := CoordinatorStatus{
		Epoch: c.epoch, Generation: c.generation,
		DesiredDigest: c.current.Digest(),
	}
	if c.active != "" {
		if entry := c.entries[c.active]; entry != nil {
			result := cloneResult(entry.result)
			status.Transaction = &result
		}
	}
	if c.last != nil {
		result := cloneResult(*c.last)
		status.LastTransaction = &result
	}
	return status
}

func (c *Coordinator) Current() *Desired {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current.clone()
}

func (c *Coordinator) execute(entry *requestEntry, transaction Transaction, deadline time.Time) {
	operationCtx, cancel := context.WithDeadline(c.ctx, deadline)
	defer cancel()
	c.setProgress(entry, StatePreparing)
	apply := c.executor.Execute(operationCtx, transaction)
	if apply.State == "" {
		if apply.Failure == nil {
			apply.State = StateCommitted
		} else {
			apply.State = StateRolledBack
		}
	}
	if !terminalExecutionState(apply.State) {
		apply = ApplyResult{
			State: StateDegraded,
			Failure: &Failure{
				Code: "invalid_executor_result", Stage: entry.state,
				Degraded: true, Message: "reconciliation executor returned a non-terminal state",
			},
		}
	}
	if operationCtx.Err() != nil && apply.Failure == nil && apply.State != StateCommitted {
		apply.Failure = &Failure{
			Code: "operation_cancelled", Stage: entry.state, Retryable: true,
			Message: operationCtx.Err().Error(),
		}
	}

	now := c.options.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	result := entry.result
	result.State = apply.State
	result.CleanupPending = apply.CleanupPending
	result.Failure = cloneFailure(apply.Failure)
	if apply.State == StateCommitted {
		c.current = transaction.Desired.clone()
		c.generation = transaction.NextGeneration
		result.Generation = c.generation
		result.Changed = true
		if result.Failure != nil {
			result.Failure.Committed = true
		}
	} else {
		result.Generation = c.generation
		if result.Failure != nil && apply.State == StateDegraded {
			result.Failure.Degraded = true
		}
	}
	c.finishLocked(entry, result, now)
	if c.active == entry.result.RequestID {
		c.active = ""
	}
}

func (c *Coordinator) setProgress(entry *requestEntry, state State) {
	if !progressState(state) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.completedAt.IsZero() {
		entry.state = state
		entry.result.State = state
	}
}

func (c *Coordinator) finishRejectedLocked(
	entry *requestEntry,
	now time.Time,
	code, message string,
	retryable bool,
	reasons []string,
) {
	result := entry.result
	result.State = StateRejected
	result.Generation = c.generation
	result.RestartReasons = append(result.RestartReasons[:0], reasons...)
	result.Failure = &Failure{
		Code: code, Stage: StateValidating, Retryable: retryable, Message: message,
	}
	c.finishLocked(entry, result, now)
}

func (c *Coordinator) finishLocked(entry *requestEntry, result Result, now time.Time) {
	completed := now.UTC()
	result.CompletedAt = &completed
	entry.state = result.State
	entry.result = cloneResult(result)
	entry.completedAt = completed
	last := cloneResult(result)
	c.last = &last
	close(entry.done)
	c.evictLocked(now)
}

func (c *Coordinator) rejectedResultLocked(requestID, code, message string, retryable bool) Result {
	completed := c.options.Now().UTC()
	return Result{
		Epoch: c.epoch, RequestID: requestID, State: StateRejected,
		Generation: c.generation, CompletedAt: &completed,
		Failure: &Failure{Code: code, Stage: StateValidating, Retryable: retryable, Message: message},
	}
}

func (c *Coordinator) operationDeadline(now, requested time.Time) time.Time {
	duration := c.options.MaxOperationTimeout
	if !requested.IsZero() {
		duration = requested.Sub(now)
	}
	if duration < c.options.MinOperationTimeout {
		duration = c.options.MinOperationTimeout
	}
	if duration > c.options.MaxOperationTimeout {
		duration = c.options.MaxOperationTimeout
	}
	return now.Add(duration)
}

func (c *Coordinator) requestFingerprint(request Request) ([sha256.Size]byte, error) {
	payload := struct {
		ExpectedEpoch      string         `json:"expected_epoch"`
		ExpectedGeneration uint64         `json:"expected_generation"`
		DeadlineMillis     int64          `json:"deadline_unix_millis"`
		Desired            *configForHash `json:"desired"`
	}{
		ExpectedEpoch:      request.ExpectedEpoch,
		ExpectedGeneration: request.ExpectedGeneration,
		Desired:            &configForHash{Config: request.Desired.config},
	}
	if !request.Deadline.IsZero() {
		payload.DeadlineMillis = request.Deadline.UnixMilli()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode reconciliation request fingerprint: %w", err)
	}
	digest := hmac.New(sha256.New, c.fingerprint)
	_, _ = digest.Write(encoded)
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

// configForHash prevents a future json.Marshaler on Desired from
// accidentally omitting secrets from the process-local collision fingerprint.
type configForHash struct {
	Config any `json:"config"`
}

func (c *Coordinator) evictLocked(now time.Time) {
	type terminalEntry struct {
		requestID string
		completed time.Time
	}
	terminal := make([]terminalEntry, 0, len(c.entries))
	for requestID, entry := range c.entries {
		if entry.completedAt.IsZero() {
			continue
		}
		if now.Sub(entry.completedAt) >= c.options.CacheTTL {
			delete(c.entries, requestID)
			continue
		}
		terminal = append(terminal, terminalEntry{requestID: requestID, completed: entry.completedAt})
	}
	if len(terminal) <= c.options.CacheEntries {
		return
	}
	sort.Slice(terminal, func(left, right int) bool {
		return terminal[left].completed.Before(terminal[right].completed)
	})
	for _, item := range terminal[:len(terminal)-c.options.CacheEntries] {
		delete(c.entries, item.requestID)
	}
}

func (c *Coordinator) waitForEntry(ctx context.Context, entry *requestEntry) (Result, error) {
	select {
	case <-entry.done:
		return cloneResult(entry.result), nil
	case <-ctx.Done():
		c.mu.Lock()
		result := cloneResult(entry.result)
		c.mu.Unlock()
		return result, ctx.Err()
	}
}

func validateRequestID(value string) error {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return errors.New("request ID must contain between 1 and 128 valid UTF-8 bytes")
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' || character == ':' {
			continue
		}
		return errors.New("request ID contains an unsupported character")
	}
	return nil
}

func progressState(state State) bool {
	return slices.Contains([]State{
		StateAccepted, StateValidating, StatePreparing, StateSwitching,
		StateCommitting, StateFinalizing, StateRollingBack,
	}, state)
}

func terminalExecutionState(state State) bool {
	return state == StateCommitted || state == StateRolledBack || state == StateDegraded
}

func cloneFailure(failure *Failure) *Failure {
	if failure == nil {
		return nil
	}
	result := *failure
	return &result
}

func cloneResult(result Result) Result {
	result.Added = append([]string(nil), result.Added...)
	result.Updated = append([]string(nil), result.Updated...)
	result.Removed = append([]string(nil), result.Removed...)
	result.RestartReasons = append([]string(nil), result.RestartReasons...)
	result.Failure = cloneFailure(result.Failure)
	if result.CompletedAt != nil {
		completed := *result.CompletedAt
		result.CompletedAt = &completed
	}
	return result
}
