package quick

import (
	"context"
	"errors"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

func TestDesktopApplyUsesCASAndNeverRetriesUnknownMutation(t *testing.T) {
	var requests []management.Request
	status := func(context.Context, string) (management.Status, error) {
		return management.Status{SupervisorEpoch: "epoch", DesiredGeneration: 3}, nil
	}
	call := func(_ context.Context, _ string, request management.Request) (management.Response, error) {
		requests = append(requests, request)
		return management.Response{}, errors.New("reply lost")
	}
	result := applyDesktopConfig(context.Background(), "wg0", "", status, call)
	if result.State != "unknown" || len(requests) != 1 {
		t.Fatalf("result=%+v requests=%+v", result, requests)
	}
	request := requests[0]
	if request.Operation != management.OperationReload || request.ExpectedEpoch != "epoch" || request.ExpectedGeneration != 3 || request.DeadlineUnixMillis == 0 {
		t.Fatalf("missing transaction constraints: %+v", request)
	}
	_ = applyDesktopConfig(context.Background(), "wg0", result.RequestID, status, call)
	if len(requests) != 2 || requests[1].Operation != management.OperationTransactionStatus || requests[1].RequestID != result.RequestID {
		t.Fatalf("unknown outcome was mutated again: %+v", requests)
	}
}

func TestDesktopApplyClassifiesResults(t *testing.T) {
	for _, test := range []struct {
		state   reconcile.State
		restart bool
		want    string
	}{
		{reconcile.StateCommitted, false, "applied"}, {reconcile.StateNoOp, false, "applied"},
		{reconcile.StateRejected, true, "restart_required"}, {reconcile.StateRolledBack, false, "failed"},
		{reconcile.StateAccepted, false, "unknown"}, {reconcile.StateDegraded, false, "failed"},
	} {
		got := desktopApplyResponse(management.Response{Result: &reconcile.Result{State: test.state, RestartRequired: test.restart}}, "id", false)
		if got.State != test.want {
			t.Fatalf("%s -> %s, want %s", test.state, got.State, test.want)
		}
	}
	if result := desktopApplyResponse(management.Response{Failure: &reconcile.Failure{Code: "not_found"}}, "id", true); result.State != "unknown" {
		t.Fatal("missing transaction was treated as proof of failure")
	}
}
