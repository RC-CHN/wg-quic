package quick

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

func RuntimeStatus(ctx context.Context, name string) (management.Status, error) {
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return management.Status{}, err
	}
	return management.NewClient(host.ManagementPath(name)).Status(ctx, name)
}

func RuntimeCall(
	ctx context.Context,
	name string,
	request management.Request,
) (management.Response, error) {
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return management.Response{}, err
	}
	if request.Interface != "" && request.Interface != name {
		return management.Response{}, errors.New("runtime request interface does not match target")
	}
	request.Interface = name
	return management.NewClient(host.ManagementPath(name)).Call(ctx, request)
}

func RuntimeEvents(
	ctx context.Context,
	name, eventStreamID string,
	afterSequence uint64,
	limit int,
) (telemetry.SessionEventBatch, error) {
	response, err := RuntimeCall(ctx, name, management.Request{
		Operation:            management.OperationEvents,
		RequiredCapabilities: []string{"session_events_v1"},
		EventStreamID:        eventStreamID, AfterSequence: afterSequence, EventLimit: limit,
	})
	if err != nil {
		return telemetry.SessionEventBatch{}, err
	}
	if response.Failure != nil {
		return telemetry.SessionEventBatch{}, errors.New(response.Failure.Message)
	}
	if response.Events == nil {
		return telemetry.SessionEventBatch{}, errors.New("management response did not include events")
	}
	return *response.Events, nil
}

func NewRuntimeRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
