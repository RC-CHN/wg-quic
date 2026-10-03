package endpoint

import (
	"context"
	"net/netip"
	"testing"
)

func TestPeerTransactionPreservesUnrelatedDNSMigration(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "commit"
		if rollback {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			resolver := &fakeResolver{responses: map[string][]Resolution{
				"unchanged.example": {
					{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10")}},
					{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.20")}},
				},
			}}
			routes := &fakeRouteLeaser{}
			specs := []PeerSpec{{PublicKey: "unchanged", Endpoint: "unchanged.example:443"}}
			s, err := NewSupervisor(specs, resolver, routes, &fakeCoreControl{}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			prepared, err := s.PreparePeerSet(ctx, "add-other-peer", PeerSetPlan{
				Peers: append(specs, PeerSpec{PublicKey: "added"}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if rollback {
				if err := prepared.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RefreshPeer(ctx, "unchanged"); err != nil {
				t.Fatal(err)
			}
			generation := s.Status()[0].Generation
			if rollback {
				err = prepared.Rollback(ctx)
			} else {
				err = prepared.Commit(ctx)
				if err == nil {
					err = prepared.Finalize(ctx)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Selected()["unchanged"]; got != netip.MustParseAddrPort("192.0.2.20:443") {
				t.Errorf("unrelated migration reverted: %v", got)
			}
			if got := s.Status()[0].Generation; got != generation {
				t.Errorf("generation reverted: %d, want %d", got, generation)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			for _, lease := range routes.leases {
				if !lease.released {
					t.Error("transaction lost ownership of migrated route")
				}
			}
		})
	}
}
