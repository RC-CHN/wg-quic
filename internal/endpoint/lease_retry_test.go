package endpoint

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestSupervisorRetriesRetiredRouteWhileRunning(t *testing.T) {
	for _, failInstall := range []bool{false, true} {
		name := "successful migration"
		if failInstall {
			name = "failed installation"
		}
		t.Run(name, func(t *testing.T) {
			first := netip.MustParseAddr("192.0.2.10")
			second := netip.MustParseAddr("192.0.2.20")
			resolver := &fakeResolver{responses: map[string][]Resolution{
				"peer.example": {
					{Addresses: []netip.Addr{first}, RefreshAfter: time.Minute},
					{Addresses: []netip.Addr{second}, RefreshAfter: time.Minute},
				},
			}}
			routes := &fakeRouteLeaser{}
			core := &fakeCoreControl{}
			supervisor := testSupervisor(t, resolver, routes, core, "peer.example:443")
			if _, err := supervisor.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			failedLeaseIndex := 0
			if failInstall {
				core.setErrors = []error{errors.New("core endpoint update failed")}
				routes.nextReleaseErrors = []error{errors.New("temporary route deletion failure")}
				failedLeaseIndex = 1
			} else {
				routes.leases[0].releaseErrors = []error{errors.New("temporary route deletion failure")}
			}
			err := supervisor.RefreshPeer(context.Background(), "peer")
			if (err != nil) != failInstall {
				t.Fatalf("migration error = %v, failInstall = %v", err, failInstall)
			}
			failedLease := routes.leases[failedLeaseIndex]
			if failedLease.releaseCalls != 1 || failedLease.released {
				t.Fatal("fixture did not leave a failed route release")
			}
			if err := supervisor.RefreshRoutes(context.Background()); err != nil {
				t.Fatal(err)
			}
			if failedLease.releaseCalls != 2 || !failedLease.released {
				t.Fatalf("running supervisor lost retired lease: %#v", failedLease)
			}
			if err := supervisor.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if failedLease.releaseCalls != 2 {
				t.Fatalf("released retired lease was retained: calls = %d", failedLease.releaseCalls)
			}
		})
	}
}

func TestSupervisorRetiredRouteRetrySurvivesAnotherFailure(t *testing.T) {
	first := netip.MustParseAddr("192.0.2.10")
	second := netip.MustParseAddr("192.0.2.20")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {
			{Addresses: []netip.Addr{first}},
			{Addresses: []netip.Addr{second}},
		},
	}}
	routes := &fakeRouteLeaser{}
	supervisor := testSupervisor(t, resolver, routes, &fakeCoreControl{}, "peer.example:443")
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := routes.leases[0]
	old.releaseErrors = []error{errors.New("first deletion failure"), errors.New("second deletion failure")}
	if err := supervisor.RefreshPeer(context.Background(), "peer"); err != nil {
		t.Fatal(err)
	}
	// Unrelated refresh work must still proceed while a retired route needs retry.
	if err := supervisor.RefreshPeer(context.Background(), "peer"); err != nil {
		t.Fatal(err)
	}
	if old.releaseCalls != 2 || old.released {
		t.Fatal("DNS refresh did not retry the pending release")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if old.releaseCalls != 3 || !old.released {
		t.Fatal("Close lost a repeatedly failing retired route")
	}
}
