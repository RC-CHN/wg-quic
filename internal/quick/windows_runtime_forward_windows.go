//go:build windows

package quick

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

func runWindowsForwardedRuntimeOperation(
	ctx context.Context,
	action string,
	name string,
) (string, error) {
	encode := func(value any) (string, error) {
		contents, err := json.Marshal(value)
		return string(contents), err
	}
	switch action {
	case "status":
		status, err := RuntimeStatus(ctx, name)
		if err != nil {
			return "", err
		}
		return encode(status)
	case "reload":
		status, err := RuntimeStatus(ctx, name)
		if err != nil {
			return "", err
		}
		requestID, err := NewRuntimeRequestID()
		if err != nil {
			return "", err
		}
		response, err := RuntimeCall(ctx, name, management.Request{
			Operation:            management.OperationReload,
			RequiredCapabilities: []string{"peer_reconcile_v1"},
			ExpectedEpoch:        status.SupervisorEpoch,
			ExpectedGeneration:   status.DesiredGeneration,
			RequestID:            requestID,
			DeadlineUnixMillis:   time.Now().Add(windowsDesktopOperationTimeout).UnixMilli(),
		})
		if err != nil {
			return "", err
		}
		contents, encodeErr := encode(response)
		if encodeErr != nil {
			return "", encodeErr
		}
		if response.Failure != nil {
			return contents, errors.New(response.Failure.Message)
		}
		if response.Result != nil && response.Result.Failure != nil &&
			response.Result.State != reconcile.StateCommitted {
			return contents, errors.New(response.Result.Failure.Message)
		}
		return contents, nil
	case "refresh-endpoints":
		response, err := RuntimeCall(ctx, name, management.Request{
			Operation:            management.OperationRefreshEndpoints,
			RequiredCapabilities: []string{"endpoint_refresh_v1"},
			DeadlineUnixMillis:   time.Now().Add(windowsDesktopOperationTimeout).UnixMilli(),
		})
		if err != nil {
			return "", err
		}
		contents, encodeErr := encode(response)
		if encodeErr != nil {
			return "", encodeErr
		}
		if response.Failure != nil {
			return contents, errors.New(response.Failure.Message)
		}
		return contents, nil
	default:
		return "", errors.New("unsupported forwarded runtime operation")
	}
}

func runWindowsCandidateReconcile(
	ctx context.Context,
	name string,
	contents []byte,
) (result string, resultErr error) {
	if err := validateWindowsManagementConfigBytes(contents); err != nil {
		return "", err
	}
	requestID, err := NewRuntimeRequestID()
	if err != nil {
		return "", err
	}
	canonicalPath := platform.Current().ConfigPath(name)
	candidatePath := filepath.Join(
		filepath.Dir(canonicalPath),
		"."+filepath.Base(canonicalPath)+".candidate-"+requestID,
	)
	if err := importWindowsDesktopConfigBytes(contents, candidatePath, false); err != nil {
		return "", fmt.Errorf("stage protected Windows reconciliation candidate: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(candidatePath); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("remove protected Windows reconciliation candidate: %w", removeErr),
			)
		}
	}()

	status, err := RuntimeStatus(ctx, name)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(windowsDesktopOperationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	response, err := RuntimeCall(ctx, name, management.Request{
		Operation:            management.OperationReconcile,
		RequiredCapabilities: []string{"peer_reconcile_v1"},
		ExpectedEpoch:        status.SupervisorEpoch,
		ExpectedGeneration:   status.DesiredGeneration,
		RequestID:            requestID,
		DeadlineUnixMillis:   deadline.UnixMilli(),
		CandidatePath:        candidatePath,
	})
	if err != nil {
		return "", err
	}
	encoded, encodeErr := json.Marshal(response)
	if encodeErr != nil {
		return "", encodeErr
	}
	result = string(encoded)
	if response.Failure != nil {
		return result, errors.New(response.Failure.Message)
	}
	if response.Result == nil {
		return result, errors.New("Windows candidate reconciliation returned no transaction result")
	}
	if response.Result.State != reconcile.StateCommitted &&
		response.Result.State != reconcile.StateNoOp {
		if response.Result.Failure != nil {
			return result, errors.New(response.Result.Failure.Message)
		}
		return result, fmt.Errorf("Windows candidate reconciliation ended in state %s", response.Result.State)
	}
	if err := importWindowsDesktopConfigBytes(contents, canonicalPath, true); err != nil {
		return result, fmt.Errorf(
			"runtime committed but canonical Windows configuration promotion failed; persistent drift remains: %w",
			err,
		)
	}
	return result, nil
}
