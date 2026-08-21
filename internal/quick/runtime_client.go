package quick

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/platform"
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

func NewRuntimeRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
