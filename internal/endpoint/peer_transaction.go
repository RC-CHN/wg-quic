package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"

	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
)

type PeerSetPlan struct {
	Peers []PeerSpec
}

type PreparedPeerSet interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	Finalize(context.Context) error
}

type endpointPeerSetState uint8

const (
	endpointPeerSetPrepared endpointPeerSetState = iota
	endpointPeerSetCommitted
	endpointPeerSetRolledBack
	endpointPeerSetFinalized
)

type preparedEndpointPeerSet struct {
	mu          sync.Mutex
	supervisor  *Supervisor
	id          string
	before      map[string]*peerState
	after       map[string]*peerState
	beforeOrder []string
	afterOrder  []string
	affected    []string
	transitions map[string]*preparedEndpointTransition
	state       endpointPeerSetState
}

type preparedEndpointTransition struct {
	publicKey         string
	before            *peerState
	after             *peerState
	candidates        []preparedEndpointCandidate
	installed         bool
	restored          bool
	coreFinalized     bool
	activeCandidate   int
	currentGeneration uint64
}

type preparedEndpointCandidate struct {
	endpoint netip.AddrPort
	lease    RouteLease
}

func (s *Supervisor) PreparePeerSet(
	ctx context.Context,
	transactionID string,
	plan PeerSetPlan,
) (PreparedPeerSet, error) {
	if transactionID == "" {
		return nil, errors.New("endpoint peer transaction ID is required")
	}
	desired, order, err := parseEndpointPeerPlan(plan)
	if err != nil {
		return nil, err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("endpoint supervisor is closed")
	}
	if s.activePeerSet != "" {
		s.mu.Unlock()
		return nil, errors.New("another endpoint peer transaction is active")
	}
	before := clonePeerStateMap(s.peers)
	beforeOrder := slices.Clone(s.order)
	for publicKey, state := range desired {
		current := before[publicKey]
		if current == nil || current.spec.Endpoint != state.spec.Endpoint {
			continue
		}
		clone := *current
		clone.spec = state.spec
		desired[publicKey] = &clone
	}
	affected := changedEndpointPeerKeys(before, desired)
	for _, publicKey := range affected {
		if owner := s.reserved[publicKey]; owner != "" {
			s.mu.Unlock()
			return nil, fmt.Errorf("peer %s is reserved by endpoint transaction %s", peerIdentifier(publicKey), owner)
		}
	}
	s.activePeerSet = transactionID
	for _, publicKey := range affected {
		s.reserved[publicKey] = transactionID
	}
	s.mu.Unlock()

	prepared := &preparedEndpointPeerSet{
		supervisor: s, id: transactionID,
		before: before, after: desired, beforeOrder: beforeOrder, afterOrder: order,
		affected: affected, transitions: make(map[string]*preparedEndpointTransition),
		state: endpointPeerSetPrepared,
	}
	for _, publicKey := range affected {
		oldState := before[publicKey]
		newState := desired[publicKey]
		transition := &preparedEndpointTransition{
			publicKey: publicKey, before: oldState, after: newState,
			activeCandidate: -1,
		}
		if oldState != nil {
			transition.currentGeneration = oldState.generation
		}
		prepared.transitions[publicKey] = transition
		if newState == nil || (oldState != nil && oldState.spec.Endpoint == newState.spec.Endpoint) {
			continue
		}
		if newState.spec.Endpoint == "" {
			// An endpoint-less peer being added does not exist in the core until
			// CommitPeerSet, so there is no endpoint to clear during prepare.
			// Existing peers still need a generation-bound clear when their
			// configured endpoint is removed.
			if oldState == nil {
				continue
			}
			generation := transition.currentGeneration + 1
			if err := s.core.ClearPeerEndpoint(ctx, publicKey, generation); err != nil {
				return prepared, fmt.Errorf("clear tentative peer endpoint: %w", err)
			}
			transition.currentGeneration = generation
			transition.installed = true
			newState.active = netip.AddrPort{}
			newState.generation = generation
			newState.lease = nil
			continue
		}
		resolution, err := s.resolve(ctx, newState)
		if err != nil {
			return prepared, fmt.Errorf("resolve tentative peer endpoint: %w", err)
		}
		newState.refreshAfter = s.refreshDelay(resolution.RefreshAfter)
		var routeErrors []error
		for _, address := range resolution.Addresses {
			lease, acquireErr := s.routes.AcquireEndpointRoute(ctx, address)
			if acquireErr != nil {
				routeErrors = append(routeErrors, acquireErr)
				continue
			}
			transition.candidates = append(transition.candidates, preparedEndpointCandidate{
				endpoint: netip.AddrPortFrom(address, newState.port), lease: lease,
			})
		}
		if len(transition.candidates) == 0 {
			return prepared, fmt.Errorf("acquire tentative endpoint routes: %w", errors.Join(routeErrors...))
		}
		if oldState != nil {
			if err := prepared.installReadyCandidate(ctx, transition); err != nil {
				return prepared, err
			}
		}
	}
	return prepared, nil
}

func parseEndpointPeerPlan(plan PeerSetPlan) (map[string]*peerState, []string, error) {
	peers := make(map[string]*peerState, len(plan.Peers))
	order := make([]string, 0, len(plan.Peers))
	for index, spec := range plan.Peers {
		if spec.PublicKey == "" {
			return nil, nil, fmt.Errorf("Peer %d public key is required", index+1)
		}
		if _, exists := peers[spec.PublicKey]; exists {
			return nil, nil, fmt.Errorf("Peer %d public key is duplicated", index+1)
		}
		state := &peerState{spec: spec}
		if spec.Endpoint != "" {
			parsed, err := peerendpoint.Parse(spec.Endpoint)
			if err != nil {
				return nil, nil, fmt.Errorf("Peer %d Endpoint: %w", index+1, err)
			}
			state.host = parsed.Host
			state.port = parsed.Port
			state.address = parsed.Address
			state.dynamic = parsed.Dynamic()
		}
		peers[spec.PublicKey] = state
		order = append(order, spec.PublicKey)
	}
	return peers, order, nil
}

func clonePeerStateMap(values map[string]*peerState) map[string]*peerState {
	result := make(map[string]*peerState, len(values))
	for publicKey, state := range values {
		clone := *state
		result[publicKey] = &clone
	}
	return result
}

func changedEndpointPeerKeys(current, desired map[string]*peerState) []string {
	result := make([]string, 0)
	for publicKey, state := range desired {
		before := current[publicKey]
		if before == nil || before.spec.Endpoint != state.spec.Endpoint {
			result = append(result, publicKey)
		}
	}
	for publicKey := range current {
		if desired[publicKey] == nil {
			result = append(result, publicKey)
		}
	}
	slices.Sort(result)
	return result
}

func (p *preparedEndpointPeerSet) installReadyCandidate(
	ctx context.Context,
	transition *preparedEndpointTransition,
) error {
	var candidateErrors []error
	for index := range transition.candidates {
		candidate := &transition.candidates[index]
		generation := transition.currentGeneration + 1
		update := PeerUpdate{
			PublicKey:  transition.publicKey,
			Endpoint:   candidate.endpoint,
			Generation: generation,
		}
		if err := p.supervisor.core.SetPeerEndpoint(ctx, update); err != nil {
			candidateErrors = append(candidateErrors, err)
			continue
		}
		transition.currentGeneration = generation
		transition.installed = true
		readyCtx, cancel := context.WithTimeout(ctx, p.supervisor.options.ReadinessTimeout)
		err := p.supervisor.core.WaitPeerReady(readyCtx, update)
		cancel()
		if err == nil {
			transition.activeCandidate = index
			transition.after.active = candidate.endpoint
			transition.after.generation = generation
			transition.after.lease = candidate.lease
			return nil
		}
		candidateErrors = append(candidateErrors, err)
		if restoreErr := p.restoreBefore(ctx, transition); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore peer endpoint between candidates: %w", restoreErr))
		}
		if finalizeErr := p.supervisor.core.FinalizePeerEndpoint(
			ctx, transition.publicKey, transition.currentGeneration,
		); finalizeErr != nil {
			return errors.Join(err, fmt.Errorf("finalize failed endpoint candidate: %w", finalizeErr))
		}
		if releaseErr := candidate.lease.Release(context.Background()); releaseErr != nil {
			return errors.Join(err, fmt.Errorf("release failed endpoint candidate: %w", releaseErr))
		}
		candidate.lease = nil
		transition.installed = false
	}
	return fmt.Errorf("no endpoint candidate authenticated: %w", errors.Join(candidateErrors...))
}

func (p *preparedEndpointPeerSet) restoreBefore(
	ctx context.Context,
	transition *preparedEndpointTransition,
) error {
	generation := transition.currentGeneration + 1
	if transition.before != nil && transition.before.active.IsValid() {
		if err := p.supervisor.core.SetPeerEndpoint(ctx, PeerUpdate{
			PublicKey:  transition.publicKey,
			Endpoint:   transition.before.active,
			Generation: generation,
		}); err != nil {
			return err
		}
	} else {
		if err := p.supervisor.core.ClearPeerEndpoint(ctx, transition.publicKey, generation); err != nil {
			return err
		}
	}
	transition.currentGeneration = generation
	if transition.before != nil {
		transition.before.generation = generation
	}
	return nil
}

func (p *preparedEndpointPeerSet) Commit(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.state {
	case endpointPeerSetCommitted, endpointPeerSetFinalized:
		return nil
	case endpointPeerSetRolledBack:
		return errors.New("rolled-back endpoint peer transaction cannot be committed")
	}
	s := p.supervisor
	s.opMu.Lock()
	defer s.opMu.Unlock()
	for _, publicKey := range p.affected {
		transition := p.transitions[publicKey]
		if transition.before != nil || transition.after == nil || transition.after.spec.Endpoint == "" {
			continue
		}
		if err := p.installReadyCandidate(ctx, transition); err != nil {
			return err
		}
	}
	s.mu.Lock()
	for _, publicKey := range p.affected {
		s.stopRefreshWorkerLocked(publicKey)
	}
	s.peers = p.after
	s.order = slices.Clone(p.afterOrder)
	for _, publicKey := range p.affected {
		s.startRefreshWorkerLocked(publicKey)
	}
	s.mu.Unlock()
	p.state = endpointPeerSetCommitted
	return nil
}

func (p *preparedEndpointPeerSet) Rollback(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.state {
	case endpointPeerSetRolledBack:
		return nil
	case endpointPeerSetFinalized:
		return errors.New("finalized endpoint peer transaction cannot be rolled back")
	}
	s := p.supervisor
	s.opMu.Lock()
	defer s.opMu.Unlock()
	var rollbackErrors []error
	for index := len(p.affected) - 1; index >= 0; index-- {
		transition := p.transitions[p.affected[index]]
		if !transition.installed {
			continue
		}
		if !transition.restored {
			if err := p.restoreBefore(ctx, transition); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"restore peer %s endpoint: %w", peerIdentifier(transition.publicKey), err,
				))
				continue
			}
			transition.restored = true
		}
		if !transition.coreFinalized {
			if err := s.core.FinalizePeerEndpoint(
				ctx, transition.publicKey, transition.currentGeneration,
			); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"finalize restored peer %s endpoint: %w", peerIdentifier(transition.publicKey), err,
				))
				continue
			}
			transition.coreFinalized = true
		}
	}
	if len(rollbackErrors) != 0 {
		return errors.Join(rollbackErrors...)
	}
	if p.state == endpointPeerSetCommitted {
		s.mu.Lock()
		for _, publicKey := range p.affected {
			s.stopRefreshWorkerLocked(publicKey)
		}
		s.peers = p.before
		s.order = slices.Clone(p.beforeOrder)
		for _, publicKey := range p.affected {
			s.startRefreshWorkerLocked(publicKey)
		}
		s.mu.Unlock()
	}
	if err := p.releaseCandidateLeases(ctx, false); err != nil {
		return err
	}
	p.releaseReservations()
	p.state = endpointPeerSetRolledBack
	return nil
}

func (p *preparedEndpointPeerSet) Finalize(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.state {
	case endpointPeerSetFinalized:
		return nil
	case endpointPeerSetPrepared:
		return errors.New("uncommitted endpoint peer transaction cannot be finalized")
	case endpointPeerSetRolledBack:
		return errors.New("rolled-back endpoint peer transaction cannot be finalized")
	}
	var cleanupErrors []error
	for _, publicKey := range p.affected {
		transition := p.transitions[publicKey]
		if !transition.installed || transition.coreFinalized {
			continue
		}
		if err := p.supervisor.core.FinalizePeerEndpoint(
			ctx, transition.publicKey, transition.currentGeneration,
		); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		} else {
			transition.coreFinalized = true
		}
	}
	if len(cleanupErrors) != 0 {
		return errors.Join(cleanupErrors...)
	}
	for _, publicKey := range p.affected {
		transition := p.transitions[publicKey]
		if transition.before == nil || transition.before.lease == nil {
			continue
		}
		if transition.after != nil && transition.after.lease == transition.before.lease {
			continue
		}
		if err := transition.before.lease.Release(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		} else {
			transition.before.lease = nil
		}
	}
	if err := p.releaseCandidateLeases(ctx, true); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if len(cleanupErrors) != 0 {
		return errors.Join(cleanupErrors...)
	}
	p.releaseReservations()
	p.state = endpointPeerSetFinalized
	return nil
}

func (p *preparedEndpointPeerSet) releaseCandidateLeases(
	ctx context.Context,
	keepActive bool,
) error {
	var errs []error
	for _, transition := range p.transitions {
		for index := range transition.candidates {
			candidate := &transition.candidates[index]
			if candidate.lease == nil || (keepActive && index == transition.activeCandidate) {
				continue
			}
			if err := candidate.lease.Release(ctx); err != nil {
				errs = append(errs, err)
			} else {
				candidate.lease = nil
			}
		}
	}
	return errors.Join(errs...)
}

func (p *preparedEndpointPeerSet) releaseReservations() {
	s := p.supervisor
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, publicKey := range p.affected {
		if s.reserved[publicKey] == p.id {
			delete(s.reserved, publicKey)
		}
	}
	if s.activePeerSet == p.id {
		s.activePeerSet = ""
	}
}
