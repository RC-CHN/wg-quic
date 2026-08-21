package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/RC-CHN/wg-quic/internal/config"
)

type peerRoutePlanner func(netip.Prefix) ([]hostOperation, error)
type peerRouteRunner func(context.Context, hostCommand) error

type commandPeerRouteManager struct {
	cfg     *config.Config
	planner peerRoutePlanner
	run     peerRouteRunner
}

type preparedCommandPeerRoutes struct {
	mu             sync.Mutex
	run            peerRouteRunner
	removals       []hostOperation
	additions      []hostOperation
	removedApplied []hostOperation
	addedApplied   []hostOperation
	rolledBack     bool
	finalized      bool
}

func newCommandPeerRouteManager(
	cfg *config.Config,
	planner peerRoutePlanner,
	runner peerRouteRunner,
) (*commandPeerRouteManager, error) {
	if cfg == nil {
		return nil, errors.New("route manager configuration is required")
	}
	if planner == nil || runner == nil {
		return nil, errors.New("route planner and runner are required")
	}
	return &commandPeerRouteManager{cfg: cfg.Clone(), planner: planner, run: runner}, nil
}

func (m *commandPeerRouteManager) Prepare(
	_ context.Context,
	plan PeerRoutePlan,
) (PreparedPeerRoutes, error) {
	additions, err := canonicalRoutePlanPrefixes(plan.Additions)
	if err != nil {
		return nil, fmt.Errorf("route additions: %w", err)
	}
	removals, err := canonicalRoutePlanPrefixes(plan.Removals)
	if err != nil {
		return nil, fmt.Errorf("route removals: %w", err)
	}
	for _, prefix := range additions {
		if slices.Contains(removals, prefix) {
			return nil, fmt.Errorf("route %s is both added and removed", prefix)
		}
	}
	prepared := &preparedCommandPeerRoutes{run: m.run}
	if strings.EqualFold(m.cfg.Interface.Table, "off") {
		return prepared, nil
	}
	automatic := m.cfg.Interface.Table == "" || strings.EqualFold(m.cfg.Interface.Table, "auto")
	for _, prefix := range append(slices.Clone(additions), removals...) {
		if automatic && prefix.Bits() == 0 {
			return nil, fmt.Errorf("automatic default-route transition requires interface restart")
		}
	}
	for _, prefix := range removals {
		operations, err := m.planner(prefix)
		if err != nil {
			return nil, err
		}
		prepared.removals = append(prepared.removals, operations...)
	}
	for _, prefix := range additions {
		operations, err := m.planner(prefix)
		if err != nil {
			return nil, err
		}
		prepared.additions = append(prepared.additions, operations...)
	}
	return prepared, nil
}

func canonicalRoutePlanPrefixes(prefixes []netip.Prefix) ([]netip.Prefix, error) {
	result := append([]netip.Prefix(nil), prefixes...)
	seen := make(map[netip.Prefix]struct{}, len(result))
	for index, prefix := range result {
		if !prefix.IsValid() {
			return nil, fmt.Errorf("prefix %d is invalid", index+1)
		}
		prefix = prefix.Masked()
		result[index] = prefix
		if _, exists := seen[prefix]; exists {
			return nil, fmt.Errorf("prefix %s is duplicated", prefix)
		}
		seen[prefix] = struct{}{}
	}
	slices.SortFunc(result, func(left, right netip.Prefix) int {
		if left.Addr().Is4() != right.Addr().Is4() {
			if left.Addr().Is4() {
				return -1
			}
			return 1
		}
		if left.Bits() != right.Bits() {
			return left.Bits() - right.Bits()
		}
		return left.Addr().Compare(right.Addr())
	})
	return result, nil
}

func (p *preparedCommandPeerRoutes) CommitRemovals(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return nil
	}
	if p.rolledBack {
		return errors.New("rolled-back route transaction cannot be committed")
	}
	for len(p.removedApplied) < len(p.removals) {
		operation := p.removals[len(p.removedApplied)]
		if err := p.run(ctx, operation.undo); err != nil {
			return err
		}
		p.removedApplied = append(p.removedApplied, operation)
	}
	return nil
}

func (p *preparedCommandPeerRoutes) CommitAdditions(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return nil
	}
	if p.rolledBack {
		return errors.New("rolled-back route transaction cannot be committed")
	}
	if len(p.removedApplied) != len(p.removals) {
		return errors.New("route removals must commit before route additions")
	}
	for len(p.addedApplied) < len(p.additions) {
		operation := p.additions[len(p.addedApplied)]
		if err := p.run(ctx, operation.apply); err != nil {
			return err
		}
		p.addedApplied = append(p.addedApplied, operation)
	}
	return nil
}

func (p *preparedCommandPeerRoutes) Rollback(ctx context.Context) error {
	if err := p.RollbackAdditions(ctx); err != nil {
		return err
	}
	return p.RollbackRemovals(ctx)
}

func (p *preparedCommandPeerRoutes) RollbackAdditions(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return errors.New("finalized route transaction cannot be rolled back")
	}
	if p.rolledBack {
		return nil
	}
	for len(p.addedApplied) > 0 {
		index := len(p.addedApplied) - 1
		operation := p.addedApplied[index]
		if err := p.run(ctx, operation.undo); err != nil {
			return err
		}
		p.addedApplied = p.addedApplied[:index]
	}
	if len(p.removedApplied) == 0 {
		p.rolledBack = true
	}
	return nil
}

func (p *preparedCommandPeerRoutes) RollbackRemovals(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return errors.New("finalized route transaction cannot be rolled back")
	}
	if p.rolledBack {
		return nil
	}
	if len(p.addedApplied) != 0 {
		return errors.New("route additions must roll back before route removals")
	}
	for len(p.removedApplied) > 0 {
		index := len(p.removedApplied) - 1
		operation := p.removedApplied[index]
		if err := p.run(ctx, operation.apply); err != nil {
			return err
		}
		p.removedApplied = p.removedApplied[:index]
	}
	p.rolledBack = true
	return nil
}

func (p *preparedCommandPeerRoutes) Finalize(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rolledBack {
		return errors.New("rolled-back route transaction cannot be finalized")
	}
	if len(p.removedApplied) != len(p.removals) || len(p.addedApplied) != len(p.additions) {
		return errors.New("route transaction is not fully committed")
	}
	p.finalized = true
	return nil
}
