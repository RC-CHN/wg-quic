//go:build !linux && !freebsd && !windows

package platform

import (
	"context"
	"errors"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/internal/platformenv"
)

var errHostNotImplemented = platformenv.ErrUnsupported

type unsupportedHost struct {
	platformenv.Paths
}

func Current() Host {
	return unsupportedHost{}
}

func (unsupportedHost) Prepare(context.Context, *config.Config) error {
	return errHostNotImplemented
}

func (unsupportedHost) NewEndpointRouteLeaser(
	context.Context,
	string,
	*config.Config,
) (endpoint.RouteLeaser, error) {
	return nil, errHostNotImplemented
}

func (unsupportedHost) ConfigureNetwork(context.Context, string, *config.Config) (Cleanup, error) {
	return nil, errHostNotImplemented
}

func (unsupportedHost) RunHook(context.Context, string, string) error {
	return errHostNotImplemented
}
