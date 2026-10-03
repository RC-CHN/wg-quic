package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
)

// The usual fake accepts every generation. Enforce the core's stale/conflicting
// generation rules here so a subsequent migration proves that rollback left
// the supervisor able to update the live core again.
type generationCheckedEndpointCore struct {
	*fakeCoreControl
	generationMu sync.Mutex
	active       map[string]PeerUpdate
}

func (c *generationCheckedEndpointCore) SetPeerEndpoint(ctx context.Context, update PeerUpdate) error {
	c.generationMu.Lock()
	defer c.generationMu.Unlock()
	previous := c.active[update.PublicKey]
	if update.Generation < previous.Generation {
		return fmt.Errorf("stale endpoint generation %d; active generation is %d", update.Generation, previous.Generation)
	}
	if update.Generation == previous.Generation && update.Endpoint != previous.Endpoint {
		return fmt.Errorf("conflicting endpoint generation %d", update.Generation)
	}
	if err := c.fakeCoreControl.SetPeerEndpoint(ctx, update); err != nil {
		return err
	}
	if c.active == nil {
		c.active = make(map[string]PeerUpdate)
	}
	c.active[update.PublicKey] = update
	return nil
}

func (c *generationCheckedEndpointCore) ClearPeerEndpoint(ctx context.Context, key string, generation uint64) error {
	return c.SetPeerEndpoint(ctx, PeerUpdate{PublicKey: key, Generation: generation})
}

func TestEndpointPeerTransactionRollbackBeforeCommitPreservesGeneration(t *testing.T) {
	for _, ready := range []bool{true, false} {
		name := "ready candidate"
		if !ready {
			name = "unready candidate"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			candidate := netip.MustParseAddrPort("192.0.2.20:443")
			core := &generationCheckedEndpointCore{fakeCoreControl: &fakeCoreControl{
				waitError: make(map[netip.AddrPort]error),
			}}
			if !ready {
				core.waitError[candidate] = errors.New("candidate did not authenticate")
			}
			resolver := &fakeResolver{responses: map[string][]Resolution{
				"changed.example": {
					{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10")}},
					{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.30")}},
				},
				"unrelated.example": {
					{Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.10")}},
					{Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.20")}},
				},
			}}
			routes := &fakeRouteLeaser{}
			specs := []PeerSpec{
				{PublicKey: "changed", Endpoint: "changed.example:443"},
				{PublicKey: "unrelated", Endpoint: "unrelated.example:443"},
			}
			s, err := NewSupervisor(specs, resolver, routes, core, Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(ctx); err != nil {
					t.Error(err)
				}
				for _, lease := range routes.leases {
					if !lease.released {
						t.Error("rollback lost ownership of a route lease")
					}
				}
			})
			if _, err := s.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			prepared, err := s.PreparePeerSet(ctx, "abort-before-commit", PeerSetPlan{Peers: []PeerSpec{
				{PublicKey: "changed", Endpoint: candidate.String()},
				specs[1],
			}})
			if (err == nil) != ready || prepared == nil {
				t.Fatalf("prepare ready=%v: transaction=%v, error=%v", ready, prepared, err)
			}
			if err := s.RefreshPeer(ctx, "unrelated"); err != nil {
				t.Fatal(err)
			}
			if err := prepared.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if got := s.peers["changed"].generation; got != core.active["changed"].Generation {
				t.Errorf("supervisor generation=%d; restored core generation=%d", got, core.active["changed"].Generation)
			}
			if got := s.Selected()["unrelated"]; got != netip.MustParseAddrPort("198.51.100.20:443") {
				t.Fatalf("rollback reverted unrelated DNS migration: %v", got)
			}
			if err := s.RefreshPeer(ctx, "changed"); err != nil {
				t.Fatalf("DNS recovery after pre-commit rollback: %v", err)
			}
			if got := s.Selected()["changed"]; got != netip.MustParseAddrPort("192.0.2.30:443") {
				t.Fatalf("subsequent DNS migration selected %v", got)
			}
		})
	}
}

func TestEndpointPeerTransactionRollbackAfterPartialPrepare(t *testing.T) {
	for _, failureIndex := range []int{0, 1} {
		t.Run(fmt.Sprintf("failure at peer %d", failureIndex+1), func(t *testing.T) {
			ctx := context.Background()
			specs := []PeerSpec{
				{PublicKey: "a", Endpoint: "192.0.2.10:443"},
				{PublicKey: "b", Endpoint: "192.0.2.11:443"},
				{PublicKey: "c", Endpoint: "192.0.2.12:443"},
			}
			s, routes := transactionalSupervisor(t, specs, &fakeCoreControl{})
			defer s.Close(ctx)
			desired := []PeerSpec{
				{PublicKey: "a", Endpoint: "192.0.2.20:443"},
				{PublicKey: "b", Endpoint: "192.0.2.21:443"},
				{PublicKey: "c", Endpoint: "192.0.2.22:443"},
			}
			desired[failureIndex].Endpoint = "missing.example:443"
			prepared, err := s.PreparePeerSet(ctx, "partial-prepare", PeerSetPlan{Peers: desired})
			if err == nil || prepared == nil {
				t.Fatalf("expected partial transaction and DNS failure: %v, %v", prepared, err)
			}
			if err := prepared.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := prepared.Rollback(ctx); err != nil {
				t.Fatalf("rollback is not idempotent: %v", err)
			}
			for _, spec := range specs {
				if got := s.Selected()[spec.PublicKey].String(); got != spec.Endpoint {
					t.Errorf("peer %s restored %s, want %s", spec.PublicKey, got, spec.Endpoint)
				}
			}
			for index, lease := range routes.leases {
				if lease.released != (index >= len(specs)) {
					t.Errorf("route %d released=%v: original routes must survive and candidates must be released", index, lease.released)
				}
			}
			if s.activePeerSet != "" || len(s.reserved) != 0 {
				t.Fatal("partial prepare retained transaction reservations")
			}
		})
	}
}
