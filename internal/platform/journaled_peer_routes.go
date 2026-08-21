package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
)

// peerRouteMutationJournal is implemented only on platforms whose tunnel and
// route objects can survive the quick process. It deliberately stores route
// projections, never peer keys or configuration secrets.
type peerRouteMutationJournal interface {
	Begin(context.Context, string, []netip.Prefix, []netip.Prefix) error
	Mark(context.Context, peerRouteJournalPhase, bool, bool) error
	Active(context.Context, []netip.Prefix) error
	Cleanup(context.Context) error
}

type journaledPeerRouteManager struct {
	inner   PeerRouteManager
	journal peerRouteMutationJournal

	mu       sync.Mutex
	current  []netip.Prefix
	inFlight bool
}

type journaledPreparedPeerRoutes struct {
	manager *journaledPeerRouteManager
	inner   PreparedPeerRoutes
	before  []netip.Prefix
	after   []netip.Prefix

	mu               sync.Mutex
	removalsApplied  bool
	additionsApplied bool
	completed        bool
}

func newJournaledPeerRouteManager(
	inner PeerRouteManager,
	journal peerRouteMutationJournal,
	initial []netip.Prefix,
) (*journaledPeerRouteManager, error) {
	if inner == nil || journal == nil {
		return nil, errors.New("peer route manager and journal are required")
	}
	canonical, err := canonicalRoutePlanPrefixes(initial)
	if err != nil {
		return nil, fmt.Errorf("initial peer route projection: %w", err)
	}
	return &journaledPeerRouteManager{
		inner: inner, journal: journal, current: canonical,
	}, nil
}

func (m *journaledPeerRouteManager) Prepare(
	ctx context.Context,
	plan PeerRoutePlan,
) (PreparedPeerRoutes, error) {
	if plan.TransactionID == "" {
		return nil, errors.New("journaled peer route transaction requires an ID")
	}
	inner, err := m.inner.Prepare(ctx, plan)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.inFlight {
		m.mu.Unlock()
		_ = inner.Rollback(context.Background())
		return nil, errors.New("another peer route transaction is in progress")
	}
	before := slices.Clone(m.current)
	after, err := applyPeerRoutePlan(before, plan)
	if err != nil {
		m.mu.Unlock()
		_ = inner.Rollback(context.Background())
		return nil, err
	}
	if err := m.journal.Begin(ctx, plan.TransactionID, before, after); err != nil {
		m.mu.Unlock()
		_ = inner.Rollback(context.Background())
		return nil, fmt.Errorf("begin peer route journal: %w", err)
	}
	m.inFlight = true
	m.mu.Unlock()
	return &journaledPreparedPeerRoutes{
		manager: m, inner: inner, before: before, after: after,
	}, nil
}

func applyPeerRoutePlan(current []netip.Prefix, plan PeerRoutePlan) ([]netip.Prefix, error) {
	additions, err := canonicalRoutePlanPrefixes(plan.Additions)
	if err != nil {
		return nil, err
	}
	removals, err := canonicalRoutePlanPrefixes(plan.Removals)
	if err != nil {
		return nil, err
	}
	projection := make(map[netip.Prefix]struct{}, len(current)+len(additions))
	for _, prefix := range current {
		projection[prefix] = struct{}{}
	}
	for _, prefix := range removals {
		if _, exists := projection[prefix]; !exists {
			return nil, fmt.Errorf("peer route removal %s is not active", prefix)
		}
		delete(projection, prefix)
	}
	for _, prefix := range additions {
		if _, exists := projection[prefix]; exists {
			return nil, fmt.Errorf("peer route addition %s is already active", prefix)
		}
		projection[prefix] = struct{}{}
	}
	result := make([]netip.Prefix, 0, len(projection))
	for prefix := range projection {
		result = append(result, prefix)
	}
	return canonicalRoutePlanPrefixes(result)
}

func (p *journaledPreparedPeerRoutes) CommitRemovals(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed || p.removalsApplied {
		return nil
	}
	if err := p.manager.journal.Mark(
		ctx, peerRouteJournalRemoving, false, false,
	); err != nil {
		return err
	}
	if err := p.inner.CommitRemovals(ctx); err != nil {
		return err
	}
	p.removalsApplied = true
	return p.manager.journal.Mark(ctx, peerRouteJournalRemoved, true, false)
}

func (p *journaledPreparedPeerRoutes) CommitAdditions(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed || p.additionsApplied {
		return nil
	}
	if !p.removalsApplied {
		return errors.New("peer route removals must commit before additions")
	}
	if err := p.manager.journal.Mark(
		ctx, peerRouteJournalAdding, true, false,
	); err != nil {
		return err
	}
	if err := p.inner.CommitAdditions(ctx); err != nil {
		return err
	}
	p.additionsApplied = true
	return p.manager.journal.Mark(ctx, peerRouteJournalCommitted, true, true)
}

func (p *journaledPreparedPeerRoutes) Rollback(ctx context.Context) error {
	if err := p.RollbackAdditions(ctx); err != nil {
		return err
	}
	return p.RollbackRemovals(ctx)
}

func (p *journaledPreparedPeerRoutes) RollbackAdditions(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed {
		return nil
	}
	if err := p.manager.journal.Mark(
		ctx, peerRouteJournalRollbackAdditions,
		p.removalsApplied, p.additionsApplied,
	); err != nil {
		return err
	}
	if err := p.inner.RollbackAdditions(ctx); err != nil {
		return err
	}
	p.additionsApplied = false
	return p.manager.journal.Mark(
		ctx, peerRouteJournalRollbackRemovals,
		p.removalsApplied, false,
	)
}

func (p *journaledPreparedPeerRoutes) RollbackRemovals(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed {
		return nil
	}
	if p.additionsApplied {
		return errors.New("peer route additions must roll back before removals")
	}
	if err := p.manager.journal.Mark(
		ctx, peerRouteJournalRollbackRemovals,
		p.removalsApplied, false,
	); err != nil {
		return err
	}
	if err := p.inner.RollbackRemovals(ctx); err != nil {
		return err
	}
	if err := p.manager.journal.Active(ctx, p.before); err != nil {
		return err
	}
	p.removalsApplied = false
	p.completed = true
	p.manager.complete(p.before)
	return nil
}

func (p *journaledPreparedPeerRoutes) Finalize(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed {
		return nil
	}
	if !p.removalsApplied || !p.additionsApplied {
		return errors.New("peer route transaction is not fully committed")
	}
	if err := p.inner.Finalize(ctx); err != nil {
		return err
	}
	if err := p.manager.journal.Active(ctx, p.after); err != nil {
		return err
	}
	p.completed = true
	p.manager.complete(p.after)
	return nil
}

func (m *journaledPeerRouteManager) complete(projection []netip.Prefix) {
	m.mu.Lock()
	m.current = slices.Clone(projection)
	m.inFlight = false
	m.mu.Unlock()
}

// Cleanup removes only exact routes whose durable journal proves ownership.
// It is called before the ordinary platform network cleanup on shutdown.
func (m *journaledPeerRouteManager) Cleanup(ctx context.Context) error {
	return m.journal.Cleanup(ctx)
}

func (m *journaledPeerRouteManager) RecoveryStatus() RecoveryStatus {
	if provider, ok := m.journal.(RecoveryStatusProvider); ok {
		return provider.RecoveryStatus()
	}
	return RecoveryStatus{State: "clean"}
}
