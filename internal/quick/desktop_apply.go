package quick

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/platformenv"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

type DesktopApplyResult struct {
	State          string   `json:"state"`
	Code           string   `json:"code,omitempty"`
	Message        string   `json:"message,omitempty"`
	RequestID      string   `json:"request_id,omitempty"`
	RestartReasons []string `json:"restart_reasons,omitempty"`
	CleanupPending bool     `json:"cleanup_pending,omitempty"`
}

// ApplyDesktopConfig applies the stored configuration once. With a request ID
// it only observes that transaction; an uncertain outcome is never retried as
// a fresh mutation. Expected transaction failures remain typed result data.
func ApplyDesktopConfig(ctx context.Context, name, requestID string) (string, error) {
	if err := (platformenv.Paths{}).ValidateInterfaceName(name); err != nil {
		return "", err
	}
	if err := validateDesktopApplyRequestID(requestID); err != nil {
		return "", err
	}
	result := applyDesktopConfig(ctx, name, requestID, RuntimeStatus, RuntimeCall)
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func validateDesktopApplyRequestID(requestID string) error {
	if requestID == "" {
		return nil
	}
	decoded, err := hex.DecodeString(requestID)
	if err != nil || len(decoded) != 16 {
		return errors.New("apply request ID must be 32 hexadecimal characters")
	}
	return nil
}

func applyDesktopConfig(ctx context.Context, name, requestID string,
	status func(context.Context, string) (management.Status, error),
	call func(context.Context, string, management.Request) (management.Response, error),
) DesktopApplyResult {
	operationCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	request := management.Request{Operation: management.OperationTransactionStatus, RequestID: requestID}
	if requestID == "" {
		current, err := status(operationCtx, name)
		if err != nil {
			return DesktopApplyResult{State: "failed", Code: "status_unavailable", Message: err.Error()}
		}
		requestID, err = NewRuntimeRequestID()
		if err != nil {
			return DesktopApplyResult{State: "failed", Code: "request_id_failed", Message: err.Error()}
		}
		deadline, _ := operationCtx.Deadline()
		request = management.Request{
			Operation: management.OperationReload, RequestID: requestID,
			ExpectedEpoch: current.SupervisorEpoch, ExpectedGeneration: current.DesiredGeneration,
			RequiredCapabilities: []string{"peer_reconcile_v1"}, DeadlineUnixMillis: deadline.UnixMilli(),
		}
	}
	response, err := call(operationCtx, name, request)
	if err != nil {
		return DesktopApplyResult{State: "unknown", Code: "outcome_unknown", Message: err.Error(), RequestID: requestID}
	}
	return desktopApplyResponse(response, requestID, request.Operation == management.OperationTransactionStatus)
}

func desktopApplyResponse(response management.Response, requestID string, observing bool) DesktopApplyResult {
	result := DesktopApplyResult{State: "failed", RequestID: requestID}
	failure := response.Failure
	if transaction := response.Result; transaction != nil {
		failure = transaction.Failure
		result.RestartReasons = transaction.RestartReasons
		result.CleanupPending = transaction.CleanupPending
		switch {
		case transaction.State == reconcile.StateCommitted || transaction.State == reconcile.StateNoOp:
			result.State = "applied"
		case transaction.RestartRequired:
			result.State = "restart_required"
		case transaction.State == reconcile.StateRejected || transaction.State == reconcile.StateRolledBack || transaction.State == reconcile.StateDegraded:
			result.State = "failed"
		default:
			result.State = "unknown"
		}
	} else if observing || failure == nil {
		result.State = "unknown"
	}
	if failure != nil {
		result.Code, result.Message = failure.Code, failure.Message
		if failure.Code == "restart_required" {
			result.State = "restart_required"
		}
	}
	return result
}
