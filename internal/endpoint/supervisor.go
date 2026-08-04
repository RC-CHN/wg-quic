package endpoint

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

type Options struct {
	MinRefresh       time.Duration
	MaxRefresh       time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	ReadinessTimeout time.Duration
	Jitter           func(time.Duration) time.Duration
	Logf             func(string, ...any)
}

type Supervisor struct {
	resolver Resolver
	routes   RouteLeaser
	core     CoreControl
	options  Options

	opMu        sync.Mutex
	mu          sync.RWMutex
	peers       map[string]*peerState
	order       []string
	initialized bool
	started     bool
	closed      bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	extraLeases []RouteLease
}

type peerState struct {
	spec         PeerSpec
	host         string
	port         uint16
	dynamic      bool
	active       netip.AddrPort
	generation   uint64
	lease        RouteLease
	refreshAfter time.Duration
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
		peers: make(map[string]*peerState, len(specs)),
		order: make([]string, 0, len(specs)),
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
			host, portText, err := net.SplitHostPort(spec.Endpoint)
			if err != nil {
				return nil, fmt.Errorf("Peer %d Endpoint: %w", index+1, err)
			}
			port, err := strconv.ParseUint(portText, 10, 16)
			if err != nil || port == 0 {
				return nil, fmt.Errorf("Peer %d Endpoint has an invalid port", index+1)
			}
			state.host = host
			state.port = uint16(port)
			if address, err := netip.ParseAddr(host); err == nil {
				state.host = address.Unmap().String()
			} else {
				state.dynamic = true
			}
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
	for _, publicKey := range s.order {
		state := s.peers[publicKey]
		if !state.dynamic {
			continue
		}
		s.wg.Add(1)
		go s.refreshLoop(runCtx, publicKey)
	}
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) Selected() map[string]netip.AddrPort {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.selectedLocked()
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
	if !state.dynamic {
		return nil
	}
	resolution, err := s.resolve(ctx, state)
	if err != nil {
		return err
	}
	state.refreshAfter = s.refreshDelay(resolution.RefreshAfter)
	for _, address := range resolution.Addresses {
		if address == state.active.Addr() {
			return nil
		}
	}

	var candidateErrors []error
	for _, address := range resolution.Addresses {
		if err := s.switchPeer(ctx, state, address); err == nil {
			return nil
		} else {
			candidateErrors = append(candidateErrors, err)
		}
	}
	return fmt.Errorf("no refreshed endpoint became ready: %w", errors.Join(candidateErrors...))
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
		state.active = newEndpoint
		state.generation = newGeneration
		state.lease = lease
		if oldLease != nil {
			if releaseErr := oldLease.Release(context.Background()); releaseErr != nil {
				s.options.Logf("release old endpoint route lease: %v", releaseErr)
			}
		}
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
	if releaseErr := lease.Release(context.Background()); releaseErr != nil {
		// Keep the failed release reachable so Close can retry it. RouteLease
		// implementations are idempotent and only mark themselves released
		// after a successful manager operation.
		s.extraLeases = append(s.extraLeases, lease)
		return errors.Join(err, fmt.Errorf("release failed endpoint route: %w", releaseErr))
	}
	return err
}

func (s *Supervisor) resolve(ctx context.Context, state *peerState) (Resolution, error) {
	if !state.dynamic {
		address, err := netip.ParseAddr(state.host)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{Addresses: []netip.Addr{address.Unmap()}}, nil
	}
	resolution, err := s.resolver.Resolve(ctx, state.host)
	if err != nil {
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
		return Resolution{}, errors.New("resolver returned no usable IP address")
	}
	return resolution, nil
}

func (s *Supervisor) refreshLoop(ctx context.Context, publicKey string) {
	defer s.wg.Done()
	failures := 0
	for {
		s.opMu.Lock()
		delay := s.peers[publicKey].refreshAfter
		s.opMu.Unlock()
		if failures > 0 {
			delay = exponentialBackoff(s.options.RetryMin, s.options.RetryMax, failures-1)
		}
		timer := time.NewTimer(s.options.Jitter(delay))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := s.RefreshPeer(ctx, publicKey); err != nil {
			failures++
			s.options.Logf("refresh peer endpoint: %v", err)
		} else {
			failures = 0
		}
	}
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
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.Stop()
	return errors.Join(s.releaseAll(ctx), s.routes.Close())
}

// Stop ends scheduled refresh work without releasing route leases. quick uses
// this before removing tunnel routes, then calls Close after host cleanup.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *Supervisor) releaseAll(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.releaseAllLocked(ctx)
}

func (s *Supervisor) releaseAllLocked(ctx context.Context) error {
	var leases []RouteLease
	for index := len(s.order) - 1; index >= 0; index-- {
		state := s.peers[s.order[index]]
		if state.lease != nil {
			leases = append(leases, state.lease)
			state.lease = nil
		}
	}
	leases = append(leases, s.extraLeases...)
	s.extraLeases = nil
	var errs []error
	for _, lease := range leases {
		if err := lease.Release(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
