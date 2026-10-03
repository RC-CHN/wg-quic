package endpoint

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type pausedEndpointResolver struct {
	*fakeResolver
	pause func()
}

func (r *pausedEndpointResolver) Resolve(ctx context.Context, host string) (Resolution, error) {
	if r.pause != nil {
		r.pause()
	}
	return r.fakeResolver.Resolve(ctx, host)
}

type pausedEndpointCore struct {
	*fakeCoreControl
	pause func()
	err   error
}

func (c *pausedEndpointCore) WaitPeerReady(context.Context, PeerUpdate) error {
	c.pause()
	return c.err
}

func TestSupervisorStatusRemainsReadableDuringEndpointRecovery(t *testing.T) {
	for _, phase := range []string{"DNS lookup", "candidate handshake", "candidate rollback", "failed candidate rollback"} {
		t.Run(phase, func(t *testing.T) {
			first := netip.MustParseAddr("192.0.2.10")
			second := netip.MustParseAddr("192.0.2.20")
			resolver := &pausedEndpointResolver{fakeResolver: &fakeResolver{responses: map[string][]Resolution{
				"peer.example": {
					{Addresses: []netip.Addr{first}},
					{Addresses: []netip.Addr{second}},
				},
			}}}
			entered := make(chan struct{})
			release := make(chan struct{})
			var unblock sync.Once
			pause := func() { close(entered); <-release }
			core := &pausedEndpointCore{fakeCoreControl: &fakeCoreControl{}, pause: func() {}}
			supervisor, err := NewSupervisor(
				[]PeerSpec{{PublicKey: "peer", Endpoint: "peer.example:443"}},
				resolver, &fakeRouteLeaser{}, core, Options{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := supervisor.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			if phase == "DNS lookup" {
				resolver.pause = pause
			} else {
				core.pause = pause
				if phase == "candidate rollback" || phase == "failed candidate rollback" {
					core.err = errors.New("candidate did not authenticate")
				}
				if phase == "failed candidate rollback" {
					core.setErrors = []error{nil, errors.New("core rejected endpoint rollback")}
				}
			}
			finished := make(chan error, 1)
			go func() { finished <- supervisor.RefreshPeer(context.Background(), "peer") }()
			t.Cleanup(func() {
				unblock.Do(func() { close(release) })
				if err := supervisor.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("refresh did not reach the blocked operation")
			}
			statuses := make(chan []Status, 16)
			for range cap(statuses) {
				go func() { statuses <- supervisor.Status() }()
			}
			deadline := time.NewTimer(250 * time.Millisecond)
			defer deadline.Stop()
			for range cap(statuses) {
				select {
				case status := <-statuses:
					if len(status) != 1 || status[0].SelectedEndpoint != first.String()+":443" || status[0].Generation != 1 {
						t.Fatalf("pending migration replaced committed status: %#v", status)
					}
					status[0].DNSCandidates[0] = "caller must not mutate the shared snapshot"
				case <-deadline.C:
					t.Fatal("status readers were blocked by endpoint recovery")
				}
			}
			if got := supervisor.Status()[0].DNSCandidates[0]; got != first.String() {
				t.Fatalf("status snapshot was aliased: %q", got)
			}
			unblock.Do(func() { close(release) })
			if err := <-finished; (err != nil) != (core.err != nil) {
				t.Fatalf("unexpected refresh result: %v", err)
			}
			status := supervisor.Status()[0]
			wantEndpoint, wantGeneration := second.String()+":443", uint64(2)
			if phase == "candidate rollback" {
				wantEndpoint, wantGeneration = first.String()+":443", 3
			}
			if status.SelectedEndpoint != wantEndpoint || status.Generation != wantGeneration || status.DNSCandidates[0] != second.String() {
				t.Fatalf("finished migration status was not published: %#v", status)
			}
		})
	}
}

func TestSupervisorStatusTracksPeerTransactionCommitAndRollback(t *testing.T) {
	supervisor, _ := transactionalSupervisor(t, []PeerSpec{{
		PublicKey: "existing", Endpoint: "192.0.2.10:443",
	}}, &fakeCoreControl{})
	defer supervisor.Close(context.Background())
	prepared, err := supervisor.PreparePeerSet(context.Background(), "status-transaction", PeerSetPlan{
		Peers: []PeerSpec{{PublicKey: "added", Endpoint: "192.0.2.20:443"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status := supervisor.Status(); len(status) != 1 || status[0].PublicKey != "existing" {
		t.Fatalf("uncommitted transaction was published: %#v", status)
	}
	if err := prepared.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := supervisor.Status(); len(status) != 1 || status[0].PublicKey != "added" || status[0].SelectedEndpoint != "192.0.2.20:443" {
		t.Fatalf("committed transaction was not published: %#v", status)
	}
	if err := prepared.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := supervisor.Status(); len(status) != 1 || status[0].PublicKey != "existing" || status[0].SelectedEndpoint != "192.0.2.10:443" {
		t.Fatalf("transaction rollback was not published: %#v", status)
	}
}
