package endpoint

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
)

type Options struct {
	MinRefresh             time.Duration
	MaxRefresh             time.Duration
	RetryMin               time.Duration
	RetryMax               time.Duration
	ReadinessTimeout       time.Duration
	NetworkDebounce        time.Duration
	HealthPoll             time.Duration
	HealthFailureThreshold uint32
	Jitter                 func(time.Duration) time.Duration
	Logf                   func(string, ...any)
}

type Supervisor struct {
	resolver Resolver
	routes   RouteLeaser
	core     CoreControl
	options  Options

	opMu          sync.Mutex
	mu            sync.RWMutex
	peers         map[string]*peerState
	order         []string
	initialized   bool
	started       bool
	closed        bool
	cancel        context.CancelFunc
	runCtx        context.Context
	workers       map[string]context.CancelFunc
	reserved      map[string]string
	activePeerSet string
	wg            sync.WaitGroup
	extraLeases   []RouteLease
}

type peerState struct {
	spec                PeerSpec
	host                string
	port                uint16
	address             netip.Addr
	dynamic             bool
	active              netip.AddrPort
	generation          uint64
	lease               RouteLease
	routeRedialPending  bool
	refreshAfter        time.Duration
	failedCandidates    map[netip.Addr]candidateFailure
	dnsCandidates       []netip.Addr
	lastResolvedAt      time.Time
	nextRefreshAt       time.Time
	lastResolutionError string
}

type Status struct {
	PublicKey           string
	ConfiguredEndpoint  string
	SelectedEndpoint    string
	DNSCandidates       []string
	LastResolvedAt      time.Time
	NextRefreshAt       time.Time
	LastResolutionError string
	Generation          uint64
}

type candidateFailure struct {
	attempts   int
	retryAfter time.Time
}

func NewSupervisor(
	specs []PeerSpec,
	resolver Resolver,
	routes RouteLeaser,
	core CoreControl,
	options Options,
) (*Supervisor, error) {
	if resolver == nil {
		return nil, errors.New("endpoint resolver is required")
	}
	if routes == nil {
		return nil, errors.New("endpoint route leaser is required")
	}
	if core == nil {
		return nil, errors.New("core endpoint control is required")
	}
	options = withDefaults(options)
	result := &Supervisor{
		resolver: resolver, routes: routes, core: core, options: options,
		peers:    make(map[string]*peerState, len(specs)),
		order:    make([]string, 0, len(specs)),
		workers:  make(map[string]context.CancelFunc),
		reserved: make(map[string]string),
	}
	for index, spec := range specs {
		if spec.PublicKey == "" {
			return nil, fmt.Errorf("Peer %d public key is required", index+1)
		}
		if _, ok := result.peers[spec.PublicKey]; ok {
			return nil, fmt.Errorf("Peer %d public key is duplicated", index+1)
		}
		state := &peerState{spec: spec}
		if spec.Endpoint != "" {
			parsed, err := peerendpoint.Parse(spec.Endpoint)
			if err != nil {
				return nil, fmt.Errorf("Peer %d Endpoint: %w", index+1, err)
			}
			state.host = parsed.Host
			state.port = parsed.Port
			state.address = parsed.Address
			state.dynamic = parsed.Dynamic()
		}
		result.peers[spec.PublicKey] = state
		result.order = append(result.order, spec.PublicKey)
	}
	return result, nil
}

func withDefaults(options Options) Options {
	if options.MinRefresh <= 0 {
		options.MinRefresh = 30 * time.Second
	}
	if options.MaxRefresh < options.MinRefresh {
		options.MaxRefresh = 30 * time.Minute
	}
	if options.RetryMin <= 0 {
		options.RetryMin = 5 * time.Second
	}
	if options.RetryMax < options.RetryMin {
		options.RetryMax = 5 * time.Minute
	}
	if options.ReadinessTimeout <= 0 {
		options.ReadinessTimeout = 15 * time.Second
	}
	if options.NetworkDebounce <= 0 {
		options.NetworkDebounce = 500 * time.Millisecond
	}
	if options.HealthPoll <= 0 {
		options.HealthPoll = 5 * time.Second
	}
	if options.HealthFailureThreshold == 0 {
		options.HealthFailureThreshold = 3
	}
	if options.Jitter == nil {
		options.Jitter = func(value time.Duration) time.Duration {
			return value * time.Duration(900+rand.IntN(201)) / 1000
		}
	}
	if options.Logf == nil {
		options.Logf = func(string, ...any) {}
	}
	return options
}

// Initialize resolves every configured endpoint, acquires its outer route, and
// installs generation 1 in a prepared core. No refresh goroutine is started
// until Activate succeeds.
func (s *Supervisor) Initialize(ctx context.Context) (map[string]netip.AddrPort, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("endpoint supervisor is closed")
	}
	if s.initialized {
		selected := s.selectedLocked()
		s.mu.Unlock()
		return selected, nil
	}
	s.mu.Unlock()

	for _, publicKey := range s.order {
		state := s.peers[publicKey]
		if state.spec.Endpoint == "" {
			continue
		}
		resolution, err := s.resolve(ctx, state)
		if err != nil {
			_ = s.releaseAllLocked(context.Background())
			return nil, fmt.Errorf("resolve peer endpoint: %w", err)
		}
		var installed bool
		var installErrors []error
		for _, address := range resolution.Addresses {
			endpoint := netip.AddrPortFrom(address, state.port)
			lease, err := s.routes.AcquireEndpointRoute(ctx, address)
			if err != nil {
				installErrors = append(installErrors, err)
				continue
			}
			update := PeerUpdate{
				PublicKey: publicKey, Endpoint: endpoint, Generation: 1,
			}
			if err := s.core.SetPeerEndpoint(ctx, update); err != nil {
				_ = lease.Release(context.Background())
				installErrors = append(installErrors, err)
				continue
			}
			state.active = endpoint
			state.generation = 1
			state.lease = lease
			state.refreshAfter = s.refreshDelay(resolution.RefreshAfter)
			installed = true
			break
		}
		if !installed {
			_ = s.releaseAllLocked(context.Background())
			return nil, fmt.Errorf("install peer endpoint %s: %w", state.spec.Endpoint, errors.Join(installErrors...))
		}
	}
	s.mu.Lock()
	s.initialized = true
	selected := s.selectedLocked()
	s.mu.Unlock()
	return selected, nil
}

func (s *Supervisor) Activate(ctx context.Context) error {
	s.mu.Lock()
	if !s.initialized {
		s.mu.Unlock()
		return errors.New("endpoint supervisor is not initialized")
	}
	if s.closed {
		s.mu.Unlock()
		return errors.New("endpoint supervisor is closed")
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.core.Activate(ctx); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return errors.New("endpoint supervisor is closed")
	}
	s.started = true
	s.cancel = cancel
	s.runCtx = runCtx
	for _, publicKey := range s.order {
		s.startRefreshWorkerLocked(publicKey)
	}
	if s.routes.Changes() != nil {
		s.wg.Add(1)
		go s.routeChangeLoop(runCtx)
	}
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) startRefreshWorkerLocked(publicKey string) {
	state := s.peers[publicKey]
	if !s.started || s.runCtx == nil || state == nil || !state.dynamic ||
		s.workers[publicKey] != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.runCtx)
	s.workers[publicKey] = cancel
	s.wg.Add(1)
	go s.refreshLoop(ctx, publicKey)
}

func (s *Supervisor) stopRefreshWorkerLocked(publicKey string) {
	if cancel := s.workers[publicKey]; cancel != nil {
		cancel()
		delete(s.workers, publicKey)
	}
}

func (s *Supervisor) Selected() map[string]netip.AddrPort {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.selectedLocked()
}

func (s *Supervisor) Status() []Status {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	result := make([]Status, 0, len(s.order))
	for _, publicKey := range s.order {
		state := s.peers[publicKey]
		if state == nil {
			continue
		}
		status := Status{
			PublicKey: publicKey, ConfiguredEndpoint: state.spec.Endpoint,
			LastResolvedAt: state.lastResolvedAt, NextRefreshAt: state.nextRefreshAt,
			LastResolutionError: state.lastResolutionError, Generation: state.generation,
		}
		if state.active.IsValid() {
			status.SelectedEndpoint = state.active.String()
		}
		for _, address := range state.dnsCandidates {
			status.DNSCandidates = append(status.DNSCandidates, address.String())
		}
		result = append(result, status)
	}
	return result
}

func (s *Supervisor) selectedLocked() map[string]netip.AddrPort {
	result := make(map[string]netip.AddrPort, len(s.peers))
	for publicKey, state := range s.peers {
		if state.active.IsValid() {
			result[publicKey] = state.active
		}
	}
	return result
}

// RefreshPeer performs one DNS refresh transaction immediately. It is also
// used by tests and administrative triggers; scheduled refreshes call the same
// implementation.
func (s *Supervisor) RefreshPeer(ctx context.Context, publicKey string) error {
	return s.refreshPeer(ctx, publicKey, false)
}

// RotatePeer resolves the configured hostname but deliberately excludes the
// currently selected address. It is used after repeated transport recovery
// failures even when DNS still returns the unhealthy address.
func (s *Supervisor) RotatePeer(ctx context.Context, publicKey string) error {
	return s.refreshPeer(ctx, publicKey, true)
}

func (s *Supervisor) refreshPeer(ctx context.Context, publicKey string, rotate bool) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return errors.New("endpoint supervisor is closed")
	}
	state, ok := s.peers[publicKey]
	if !ok {
		return errors.New("peer public key is not configured")
	}
	s.mu.RLock()
	reserved := s.reserved[publicKey]
	s.mu.RUnlock()
	if reserved != "" {
		return errors.New("peer endpoint is reserved by a reconciliation transaction")
	}
	if !state.dynamic {
		return nil
	}
	resolution, err := s.resolve(ctx, state)
	if err != nil {
		return err
	}
	state.refreshAfter = s.refreshDelay(resolution.RefreshAfter)
	for _, address := range resolution.Addresses {
		if !rotate && address == state.active.Addr() {
			return nil
		}
	}

	var candidateErrors []error
	for _, address := range resolution.Addresses {
		if rotate && address == state.active.Addr() {
			continue
		}
		if failure := state.failedCandidates[address]; !failure.retryAfter.IsZero() &&
			time.Now().Before(failure.retryAfter) {
			continue
		}
		if err := s.switchPeer(ctx, state, address); err == nil {
			delete(state.failedCandidates, address)
			return nil
		} else {
			candidateErrors = append(candidateErrors, err)
			if state.failedCandidates == nil {
				state.failedCandidates = make(map[netip.Addr]candidateFailure)
			}
			failure := state.failedCandidates[address]
			failure.attempts++
			failure.retryAfter = time.Now().Add(exponentialBackoff(
				s.options.RetryMin, s.options.RetryMax, failure.attempts-1,
			))
			state.failedCandidates[address] = failure
		}
	}
	if len(candidateErrors) == 0 {
		return errors.New("no alternate endpoint candidate is currently eligible")
	}
	return fmt.Errorf("no refreshed endpoint became ready: %w", errors.Join(candidateErrors...))
}

// RefreshAll performs one administrative DNS refresh for every dynamic peer.
// Each peer uses the same endpoint transaction as its scheduled worker.
func (s *Supervisor) RefreshAll(ctx context.Context) error {
	var errs []error
	for _, publicKey := range s.dynamicPeerKeys() {
		if err := s.RefreshPeer(ctx, publicKey); err != nil {
			errs = append(errs, fmt.Errorf(
				"refresh peer %s endpoint: %w",
				peerIdentifier(publicKey), err,
			))
		}
	}
	return errors.Join(errs...)
}

func (s *Supervisor) switchPeer(ctx context.Context, state *peerState, address netip.Addr) error {
	lease, err := s.routes.AcquireEndpointRoute(ctx, address)
	if err != nil {
		return err
	}
	newEndpoint := netip.AddrPortFrom(address, state.port)
	newGeneration := state.generation + 1
	update := PeerUpdate{
		PublicKey: state.spec.PublicKey, Endpoint: newEndpoint, Generation: newGeneration,
	}
	if err := s.core.SetPeerEndpoint(ctx, update); err != nil {
		_ = lease.Release(context.Background())
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, s.options.ReadinessTimeout)
	err = s.core.WaitPeerReady(readyCtx, update)
	cancel()
	if err == nil {
		oldLease := state.lease
		oldEndpoint := state.active
		state.active = newEndpoint
		state.generation = newGeneration
		state.lease = lease
		if finalizeErr := s.core.FinalizePeerEndpoint(
			ctx, state.spec.PublicKey, newGeneration,
		); finalizeErr != nil {
			if oldLease != nil {
				s.extraLeases = append(s.extraLeases, oldLease)
			}
			return fmt.Errorf("finalize migrated peer endpoint: %w", finalizeErr)
		}
		if oldLease != nil {
			if releaseErr := oldLease.Release(context.Background()); releaseErr != nil {
				s.options.Logf("release old endpoint route lease: %v", releaseErr)
			}
		}
		s.options.Logf(
			"peer %s endpoint migrated: %s -> %s",
			peerIdentifier(state.spec.PublicKey), oldEndpoint, newEndpoint,
		)
		return nil
	}

	rollback := PeerUpdate{
		PublicKey:  state.spec.PublicKey,
		Endpoint:   state.active,
		Generation: newGeneration + 1,
	}
	if rollbackErr := s.core.SetPeerEndpoint(ctx, rollback); rollbackErr != nil {
		// The core may still be using the new endpoint. Retain both route
		// leases and adopt the new generation so cleanup remains safe.
		if state.lease != nil {
			s.extraLeases = append(s.extraLeases, state.lease)
		}
		state.active = newEndpoint
		state.generation = newGeneration
		state.lease = lease
		return errors.Join(err, fmt.Errorf("rollback peer endpoint: %w", rollbackErr))
	}
	state.generation = rollback.Generation
	if finalizeErr := s.core.FinalizePeerEndpoint(
		ctx, state.spec.PublicKey, rollback.Generation,
	); finalizeErr != nil {
		s.extraLeases = append(s.extraLeases, lease)
		return errors.Join(err, fmt.Errorf("finalize rolled-back peer endpoint: %w", finalizeErr))
	}
	if releaseErr := lease.Release(context.Background()); releaseErr != nil {
		// Keep the failed release reachable so Close can retry it. RouteLease
		// implementations are idempotent and only mark themselves released
		// after a successful manager operation.
		s.extraLeases = append(s.extraLeases, lease)
		return errors.Join(err, fmt.Errorf("release failed endpoint route: %w", releaseErr))
	}
	return err
}

// RefreshRoutes asks each platform route lease to re-evaluate its path. DNS
// selection is deliberately not part of this transaction: the peer keeps the
// same numeric endpoint, while a changed outer path causes a transport redial.
func (s *Supervisor) RefreshRoutes(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return errors.New("endpoint supervisor is closed")
	}
	var errs []error
	for _, publicKey := range s.order {
		state := s.peers[publicKey]
		s.mu.RLock()
		reserved := s.reserved[publicKey]
		s.mu.RUnlock()
		if reserved != "" {
			continue
		}
		lease, ok := state.lease.(RefreshableRouteLease)
		if !ok || lease == nil {
			continue
		}
		changed, err := lease.Refresh(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("refresh peer outer route: %w", err))
			continue
		}
		if changed {
			state.routeRedialPending = true
		}
		if !state.routeRedialPending {
			continue
		}
		if err := s.core.RedialPeer(ctx, publicKey); err != nil {
			errs = append(errs, fmt.Errorf("redial peer after outer route change: %w", err))
		} else {
			state.routeRedialPending = false
			s.options.Logf(
				"peer %s outer route changed; transport redial requested",
				peerIdentifier(publicKey),
			)
		}
	}
	return errors.Join(errs...)
}

func peerIdentifier(publicKey string) string {
	if len(publicKey) <= 8 {
		return publicKey
	}
	return publicKey[:8]
}

func (s *Supervisor) resolve(ctx context.Context, state *peerState) (Resolution, error) {
	if !state.dynamic {
		state.dnsCandidates = []netip.Addr{state.address}
		state.lastResolutionError = ""
		return Resolution{Addresses: []netip.Addr{state.address}}, nil
	}
	resolution, err := s.resolver.Resolve(ctx, state.host)
	if err != nil {
		state.lastResolutionError = err.Error()
		return Resolution{}, err
	}
	seen := make(map[netip.Addr]struct{}, len(resolution.Addresses))
	filtered := resolution.Addresses[:0]
	for _, address := range resolution.Addresses {
		address = address.Unmap()
		if !address.IsValid() || address.IsUnspecified() {
			continue
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		filtered = append(filtered, address)
	}
	resolution.Addresses = filtered
	if len(resolution.Addresses) == 0 {
		err := errors.New("resolver returned no usable IP address")
		state.lastResolutionError = err.Error()
		return Resolution{}, err
	}
	state.dnsCandidates = append(state.dnsCandidates[:0], resolution.Addresses...)
	state.lastResolvedAt = time.Now()
	state.lastResolutionError = ""
	return resolution, nil
}

func (s *Supervisor) refreshLoop(ctx context.Context, publicKey string) {
	defer s.wg.Done()
	dnsFailures := 0
	dnsDelay := func() (time.Duration, bool) {
		s.opMu.Lock()
		defer s.opMu.Unlock()
		state := s.peers[publicKey]
		if state == nil {
			return 0, false
		}
		delay := state.refreshAfter
		if dnsFailures > 0 {
			delay = exponentialBackoff(s.options.RetryMin, s.options.RetryMax, dnsFailures-1)
		}
		delay = s.options.Jitter(delay)
		state.nextRefreshAt = time.Now().Add(delay)
		return delay, true
	}
	delay, exists := dnsDelay()
	if !exists {
		return
	}
	dnsTimer := time.NewTimer(delay)
	healthTimer := time.NewTimer(s.options.Jitter(s.options.HealthPoll))
	defer dnsTimer.Stop()
	defer healthTimer.Stop()
	resetTimer := func(timer *time.Timer, delay time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-dnsTimer.C:
			if err := s.RefreshPeer(ctx, publicKey); err != nil {
				dnsFailures++
				s.options.Logf("refresh peer endpoint: %v", err)
			} else {
				dnsFailures = 0
			}
			delay, exists := dnsDelay()
			if !exists {
				return
			}
			resetTimer(dnsTimer, delay)
		case <-healthTimer.C:
			health, err := s.core.PeerHealth(ctx, publicKey)
			if err != nil {
				s.options.Logf("inspect peer endpoint health: %v", err)
			} else if health.ConsecutiveReconnectFailures >= s.options.HealthFailureThreshold {
				if err := s.RotatePeer(ctx, publicKey); err != nil {
					s.options.Logf("rotate unhealthy peer endpoint: %v", err)
				} else {
					dnsFailures = 0
					delay, exists := dnsDelay()
					if !exists {
						return
					}
					resetTimer(dnsTimer, delay)
				}
			}
			resetTimer(healthTimer, s.options.Jitter(s.options.HealthPoll))
		}
	}
}

func (s *Supervisor) routeChangeLoop(ctx context.Context) {
	defer s.wg.Done()
	changes := s.routes.Changes()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
		}

		if !s.debounceNetworkChanges(ctx, changes) {
			return
		}
		failures := 0
		for {
			err := s.reconcileNetworkChange(ctx)
			if err == nil || ctx.Err() != nil {
				break
			}
			failures++
			s.options.Logf(
				"reconcile peer paths after network change (attempt %d): %v",
				failures,
				err,
			)
			delay := exponentialBackoff(
				s.options.RetryMin,
				s.options.RetryMax,
				failures-1,
			)
			timer := time.NewTimer(s.options.Jitter(delay))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case _, ok := <-changes:
				if !timer.Stop() {
					<-timer.C
				}
				if !ok {
					return
				}
				if !s.debounceNetworkChanges(ctx, changes) {
					return
				}
				failures = 0
			case <-timer.C:
			}
		}
	}
}

// debounceNetworkChanges waits until the route notification stream has been
// quiet for NetworkDebounce. One notification has already been consumed by
// the caller.
func (s *Supervisor) debounceNetworkChanges(
	ctx context.Context,
	changes <-chan struct{},
) bool {
	timer := time.NewTimer(s.options.NetworkDebounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case _, ok := <-changes:
			if !ok {
				return false
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(s.options.NetworkDebounce)
		case <-timer.C:
			return true
		}
	}
}

func (s *Supervisor) reconcileNetworkChange(ctx context.Context) error {
	var errs []error
	if err := s.RefreshRoutes(ctx); err != nil {
		errs = append(errs, err)
	}
	for _, publicKey := range s.dynamicPeerKeys() {
		if err := s.RefreshPeer(ctx, publicKey); err != nil {
			errs = append(errs, fmt.Errorf("re-resolve peer endpoint: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (s *Supervisor) dynamicPeerKeys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.order))
	for _, publicKey := range s.order {
		if s.peers[publicKey].dynamic {
			result = append(result, publicKey)
		}
	}
	return result
}

func (s *Supervisor) refreshDelay(value time.Duration) time.Duration {
	if value < s.options.MinRefresh {
		return s.options.MinRefresh
	}
	if value > s.options.MaxRefresh {
		return s.options.MaxRefresh
	}
	return value
}

func exponentialBackoff(minimum, maximum time.Duration, exponent int) time.Duration {
	value := minimum
	for range exponent {
		if value >= maximum/2 {
			return maximum
		}
		value *= 2
	}
	return min(value, maximum)
}

func (s *Supervisor) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if err := s.StopContext(ctx); err != nil {
		return err
	}
	if err := s.releaseAll(ctx); err != nil {
		// Keep the route manager open and every failed lease reachable. A
		// subsequent Close call can retry the exact same ownership release.
		return err
	}
	return s.routes.Close()
}

// Stop ends scheduled refresh work without releasing route leases. quick uses
// this before removing tunnel routes, then calls Close after host cleanup.
func (s *Supervisor) Stop() {
	_ = s.StopContext(context.Background())
}

// StopContext ends scheduled refresh work without releasing route leases, but
// lets shutdown callers impose a deadline. If the deadline expires, Close must
// not release leases that a stuck refresh worker could still be using.
func (s *Supervisor) StopContext(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.runCtx = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.mu.Lock()
		clear(s.workers)
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for endpoint refresh workers: %w", ctx.Err())
	}
}

func (s *Supervisor) releaseAll(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.releaseAllLocked(ctx)
}

func (s *Supervisor) releaseAllLocked(ctx context.Context) error {
	var errs []error
	for index := len(s.order) - 1; index >= 0; index-- {
		state := s.peers[s.order[index]]
		if state.lease == nil {
			continue
		}
		if err := state.lease.Release(ctx); err != nil {
			errs = append(errs, err)
		} else {
			state.lease = nil
		}
	}
	failedExtra := s.extraLeases[:0]
	for _, lease := range s.extraLeases {
		if err := lease.Release(ctx); err != nil {
			errs = append(errs, err)
			failedExtra = append(failedExtra, lease)
		}
	}
	s.extraLeases = failedExtra
	return errors.Join(errs...)
}
