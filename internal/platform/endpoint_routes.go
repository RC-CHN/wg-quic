package platform

import (
	"context"
	"net/netip"

	"github.com/RC-CHN/wg-quic/internal/endpoint"
)

type noopEndpointRouteLeaser struct{}
type noopEndpointRouteLease struct{}

func (noopEndpointRouteLeaser) AcquireEndpointRoute(
	context.Context,
	netip.Addr,
) (endpoint.RouteLease, error) {
	return noopEndpointRouteLease{}, nil
}

func (noopEndpointRouteLeaser) Close() error { return nil }

func (noopEndpointRouteLeaser) Changes() <-chan struct{} { return nil }

func (noopEndpointRouteLease) Release(context.Context) error { return nil }
