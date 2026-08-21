package platform

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
)

type peerRouteRecorder struct {
	commands []string
	failNext map[string]error
}

func (r *peerRouteRecorder) run(_ context.Context, command hostCommand) error {
	value := command.name + " " + strings.Join(command.args, " ")
	if err := r.failNext[value]; err != nil {
		delete(r.failNext, value)
		return err
	}
	r.commands = append(r.commands, value)
	return nil
}

func testPeerRouteManager(t *testing.T, table string, recorder *peerRouteRecorder) *commandPeerRouteManager {
	t.Helper()
	manager, err := newCommandPeerRouteManager(
		&config.Config{Interface: config.Interface{Table: table}},
		func(prefix netip.Prefix) ([]hostOperation, error) {
			return []hostOperation{{
				apply: hostCommand{name: "route-add", args: []string{prefix.String()}},
				undo:  hostCommand{name: "route-delete", args: []string{prefix.String()}},
			}}, nil
		},
		recorder.run,
	)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestPeerRouteTransactionCommitOrderAndFinalize(t *testing.T) {
	recorder := &peerRouteRecorder{failNext: make(map[string]error)}
	manager := testPeerRouteManager(t, "auto", recorder)
	prepared, err := manager.Prepare(context.Background(), PeerRoutePlan{
		Removals:  []netip.Prefix{netip.MustParsePrefix("10.1.0.9/16")},
		Additions: []netip.Prefix{netip.MustParsePrefix("10.2.0.9/16")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.commands) != 0 {
		t.Fatal("route preparation mutated host state")
	}
	if err := prepared.CommitRemovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitAdditions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recorder.commands, []string{
		"route-delete 10.1.0.0/16",
		"route-add 10.2.0.0/16",
	}) {
		t.Fatalf("route commands = %#v", recorder.commands)
	}
	if err := prepared.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize is not idempotent: %v", err)
	}
}

func TestPeerRouteTransactionRollbackIsRetryable(t *testing.T) {
	recorder := &peerRouteRecorder{failNext: make(map[string]error)}
	manager := testPeerRouteManager(t, "auto", recorder)
	prepared, err := manager.Prepare(context.Background(), PeerRoutePlan{
		Removals:  []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
		Additions: []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitRemovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitAdditions(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorder.failNext["route-delete 10.2.0.0/16"] = errors.New("temporary failure")
	if err := prepared.Rollback(context.Background()); err == nil {
		t.Fatal("rollback failure was hidden")
	}
	if err := prepared.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback retry failed: %v", err)
	}
	if !slices.Equal(recorder.commands, []string{
		"route-delete 10.1.0.0/16",
		"route-add 10.2.0.0/16",
		"route-delete 10.2.0.0/16",
		"route-add 10.1.0.0/16",
	}) {
		t.Fatalf("route rollback commands = %#v", recorder.commands)
	}
}

func TestPeerRouteTransactionRejectsAutomaticDefaultAndDuplicatePlans(t *testing.T) {
	recorder := &peerRouteRecorder{failNext: make(map[string]error)}
	manager := testPeerRouteManager(t, "auto", recorder)
	if _, err := manager.Prepare(context.Background(), PeerRoutePlan{
		Additions: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	}); err == nil {
		t.Fatal("automatic default-route transition was accepted")
	}
	if _, err := manager.Prepare(context.Background(), PeerRoutePlan{
		Additions: []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"),
			netip.MustParsePrefix("10.0.0.1/8"),
		},
	}); err == nil {
		t.Fatal("duplicate route plan prefix was accepted")
	}
}

func TestPeerRouteTransactionTableOffIsANoOp(t *testing.T) {
	recorder := &peerRouteRecorder{failNext: make(map[string]error)}
	manager := testPeerRouteManager(t, "off", recorder)
	prepared, err := manager.Prepare(context.Background(), PeerRoutePlan{
		Additions: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitRemovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitAdditions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.commands) != 0 {
		t.Fatalf("Table=off commands = %#v", recorder.commands)
	}
}

func TestPeerRouteTransactionRollbackIsTerminal(t *testing.T) {
	recorder := &peerRouteRecorder{failNext: make(map[string]error)}
	manager := testPeerRouteManager(t, "auto", recorder)
	prepared, err := manager.Prepare(context.Background(), PeerRoutePlan{
		Additions: []netip.Prefix{netip.MustParsePrefix("10.80.0.0/16")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback is not idempotent: %v", err)
	}
	if err := prepared.CommitRemovals(context.Background()); err == nil {
		t.Fatal("rolled-back route transaction was recommitted")
	}
	if err := prepared.Finalize(context.Background()); err == nil {
		t.Fatal("rolled-back route transaction was finalized")
	}
}
