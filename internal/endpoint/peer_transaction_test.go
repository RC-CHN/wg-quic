package endpoint

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func transactionalSupervisor(
	t *testing.T,
	specs []PeerSpec,
	core *fakeCoreControl,
) (*Supervisor, *fakeRouteLeaser) {
	t.Helper()
	routes := &fakeRouteLeaser{}
	supervisor, err := NewSupervisor(
		specs,
		&fakeResolver{responses: make(map[string][]Resolution)},
		routes,
		core,
		Options{ReadinessTimeout: time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return supervisor, routes
}

func TestEndpointPeerTransactionPreparesThenPublishesAndFinalizes(t *testing.T) {
	core := &fakeCoreControl{waitError: make(map[netip.AddrPort]error)}
	supervisor, routes := transactionalSupervisor(t, []PeerSpec{{
		PublicKey: "existing", Endpoint: "192.0.2.10:443",
	}}, core)
	prepared, err := supervisor.PreparePeerSet(context.Background(), "transaction-1", PeerSetPlan{
		Peers: []PeerSpec{
			{PublicKey: "existing", Endpoint: "192.0.2.20:443"},
			{PublicKey: "added", Endpoint: "192.0.2.30:443"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := supervisor.Selected(); len(got) != 1 ||
		got["existing"] != netip.MustParseAddrPort("192.0.2.10:443") {
		t.Fatalf("prepare published tentative endpoints: %#v", got)
	}
	if len(core.updates) != 2 || core.updates[1].Endpoint != netip.MustParseAddrPort("192.0.2.20:443") ||
		core.updates[1].Generation != 2 {
		t.Fatalf("prepare core updates = %#v", core.updates)
	}
	if err := prepared.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantSelected := map[string]netip.AddrPort{
		"existing": netip.MustParseAddrPort("192.0.2.20:443"),
		"added":    netip.MustParseAddrPort("192.0.2.30:443"),
	}
	if got := supervisor.Selected(); !mapsEqualEndpoints(got, wantSelected) {
		t.Fatalf("committed endpoints = %#v", got)
	}
	if len(core.updates) != 3 || core.updates[2].PublicKey != "added" ||
		core.updates[2].Generation != 1 {
		t.Fatalf("commit core updates = %#v", core.updates)
	}
	if err := prepared.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(core.finalized, []PeerUpdate{
		{PublicKey: "added", Generation: 1},
		{PublicKey: "existing", Generation: 2},
	}) {
		t.Fatalf("finalized core endpoints = %#v", core.finalized)
	}
	if !routes.leases[0].released || routes.leases[1].released || routes.leases[2].released {
		t.Fatalf("finalized route ownership = %#v", routes.leases)
	}
	if supervisor.activePeerSet != "" || len(supervisor.reserved) != 0 {
		t.Fatal("finalization retained endpoint reservations")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointPeerTransactionRollbackRestoresPublishedProjection(t *testing.T) {
	core := &fakeCoreControl{waitError: make(map[netip.AddrPort]error)}
	supervisor, routes := transactionalSupervisor(t, []PeerSpec{{
		PublicKey: "existing", Endpoint: "192.0.2.10:443",
	}}, core)
	prepared, err := supervisor.PreparePeerSet(context.Background(), "transaction-2", PeerSetPlan{
		Peers: []PeerSpec{
			{PublicKey: "existing", Endpoint: "192.0.2.20:443"},
			{PublicKey: "added", Endpoint: "192.0.2.30:443"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback is not idempotent: %v", err)
	}
	selected := supervisor.Selected()
	if len(selected) != 1 || selected["existing"] != netip.MustParseAddrPort("192.0.2.10:443") {
		t.Fatalf("rolled-back endpoints = %#v", selected)
	}
	if len(core.updates) != 5 || core.updates[3].PublicKey != "existing" ||
		core.updates[3].Endpoint != netip.MustParseAddrPort("192.0.2.10:443") ||
		core.updates[3].Generation != 3 || core.updates[4].PublicKey != "added" ||
		core.updates[4].Endpoint.IsValid() || core.updates[4].Generation != 2 {
		t.Fatalf("rollback core updates = %#v", core.updates)
	}
	if routes.leases[0].released || !routes.leases[1].released || !routes.leases[2].released {
		t.Fatalf("rollback route ownership = %#v", routes.leases)
	}
	if !slices.Equal(core.finalized, []PeerUpdate{
		{PublicKey: "existing", Generation: 3},
		{PublicKey: "added", Generation: 2},
	}) {
		t.Fatalf("rollback finalized core endpoints = %#v", core.finalized)
	}
	if err := prepared.Commit(context.Background()); err == nil {
		t.Fatal("rolled-back endpoint transaction was recommitted")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointPeerTransactionUnreadyAddedPeerRollsBackBeforePublication(t *testing.T) {
	candidate := netip.MustParseAddrPort("192.0.2.30:443")
	core := &fakeCoreControl{waitError: map[netip.AddrPort]error{
		candidate: errors.New("authenticated readiness timeout"),
	}}
	supervisor, routes := transactionalSupervisor(t, nil, core)
	prepared, err := supervisor.PreparePeerSet(context.Background(), "transaction-3", PeerSetPlan{
		Peers: []PeerSpec{{PublicKey: "added", Endpoint: candidate.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(context.Background()); err == nil {
		t.Fatal("unready added peer was committed")
	}
	if err := prepared.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(supervisor.Selected()) != 0 || !routes.leases[0].released {
		t.Fatalf("failed candidate leaked publication or route: %#v", supervisor.Selected())
	}
	if !slices.EqualFunc(core.updates, []PeerUpdate{
		{PublicKey: "added", Endpoint: candidate, Generation: 1},
		{PublicKey: "added", Generation: 2},
	}, func(left, right PeerUpdate) bool { return left == right }) {
		t.Fatalf("failed candidate core updates = %#v", core.updates)
	}
	if !slices.Equal(core.finalized, []PeerUpdate{{PublicKey: "added", Generation: 2}}) {
		t.Fatalf("failed candidate finalization = %#v", core.finalized)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointPeerTransactionAddsListeningPeerWithoutPrematureCoreClear(t *testing.T) {
	core := &fakeCoreControl{waitError: make(map[netip.AddrPort]error)}
	supervisor, routes := transactionalSupervisor(t, nil, core)
	prepared, err := supervisor.PreparePeerSet(context.Background(), "transaction-listener", PeerSetPlan{
		Peers: []PeerSpec{{PublicKey: "listener"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(core.updates) != 0 {
		t.Fatalf("endpoint-less peer prepare touched the not-yet-committed core peer: %#v", core.updates)
	}
	if len(routes.leases) != 0 {
		t.Fatalf("endpoint-less peer acquired route leases: %#v", routes.leases)
	}
	if err := prepared.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, exists := supervisor.peers["listener"]; !exists {
		t.Fatal("committed listening peer was not published")
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func mapsEqualEndpoints(left, right map[string]netip.AddrPort) bool {
	if len(left) != len(right) {
		return false
	}
	for publicKey, endpoint := range left {
		if right[publicKey] != endpoint {
			return false
		}
	}
	return true
}
