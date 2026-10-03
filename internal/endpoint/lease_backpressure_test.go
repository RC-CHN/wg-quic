package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"
)

func TestSupervisorBoundsFailedMigrationCleanupAndResumes(t *testing.T) {
	for _, failInstall := range []bool{false, true} {
		t.Run(fmt.Sprintf("install_failure_%t", failInstall), func(t *testing.T) {
			ctx := context.Background()
			resolver := &fakeResolver{responses: map[string][]Resolution{
				"peer.example": {{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.1")}}},
			}}
			routes := &fakeRouteLeaser{}
			core := &fakeCoreControl{}
			s := testSupervisor(t, resolver, routes, core, "peer.example:443")
			if _, err := s.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			cleanupFailure := errors.New("route deletion unavailable")
			for attempt := 0; attempt < maxPendingMigrationLeases*3; attempt++ {
				// Every iteration gets a fresh DNS answer: candidate backoff
				// cannot accidentally provide the resource bound under test.
				resolver.responses["peer.example"] = []Resolution{{Addresses: []netip.Addr{
					netip.AddrFrom4([4]byte{198, 51, 100, byte(attempt + 1)}),
				}}}
				for _, lease := range routes.leases {
					if !lease.released {
						lease.releaseErrors = []error{cleanupFailure}
					}
				}
				if failInstall {
					core.setErrors = []error{errors.New("core update failed")}
					routes.nextReleaseErrors = []error{cleanupFailure}
				}
				err := s.RefreshPeer(ctx, "peer")
				if attempt >= maxPendingMigrationLeases && !errors.Is(err, errMigrationCleanupPending) {
					t.Fatalf("attempt %d did not apply cleanup backpressure: %v", attempt, err)
				}
			}
			if len(routes.acquired) != maxPendingMigrationLeases+1 || len(s.retiredLeases) != maxPendingMigrationLeases {
				t.Fatalf("unbounded cleanup: acquired %d, retained %d", len(routes.acquired), len(s.retiredLeases))
			}
			if len(s.peers["peer"].failedCandidates) != 0 {
				t.Fatal("local cleanup failure incorrectly backed off the current DNS candidate")
			}
			if s.peers["peer"].lease == nil {
				t.Fatal("backpressure discarded the live route")
			}
			for _, lease := range routes.leases {
				lease.releaseErrors = nil
			}
			routes.nextReleaseErrors = nil
			core.setErrors = nil
			if err := s.RefreshPeer(ctx, "peer"); err != nil {
				t.Fatalf("migration did not resume when cleanup recovered: %v", err)
			}
			if len(s.retiredLeases) != 0 || len(routes.acquired) != maxPendingMigrationLeases+2 {
				t.Fatal("recovery did not reconcile all ownership and install the current candidate")
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			for _, lease := range routes.leases {
				if !lease.released {
					t.Fatalf("lost route ownership for %s", lease.address)
				}
			}
		})
	}
}

func TestMigrationBackpressureAlsoCountsUnfinalizedRoutes(t *testing.T) {
	routes := &fakeRouteLeaser{}
	s := testSupervisor(t, &fakeResolver{}, routes, &fakeCoreControl{}, "192.0.2.1:443")
	if _, err := s.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range maxPendingMigrationLeases {
		lease, err := routes.AcquireEndpointRoute(context.Background(), netip.MustParseAddr("192.0.2.2"))
		if err != nil {
			t.Fatal(err)
		}
		s.extraLeases = append(s.extraLeases, lease)
	}
	if err := s.switchPeer(context.Background(), s.peers["peer"], netip.MustParseAddr("192.0.2.3")); !errors.Is(err, errMigrationCleanupPending) {
		t.Fatalf("unsafe generations did not stop further acquisition: %v", err)
	}
	if len(routes.acquired) != maxPendingMigrationLeases+1 {
		t.Fatal("migration acquired a route despite a full cleanup backlog")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, lease := range routes.leases {
		if !lease.released {
			t.Fatal("shutdown lost retained route ownership")
		}
	}
}
