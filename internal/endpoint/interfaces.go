// Package endpoint owns platform-independent endpoint resolution and
// transactional peer migration for wg-quic-quick.
package endpoint

import (
	"context"
	"net/netip"
	"time"
)

type Resolution struct {
	Addresses    []netip.Addr
	RefreshAfter time.Duration
}

type Resolver interface {
	Resolve(context.Context, string) (Resolution, error)
}

// RouteLease proves that the outer route for one numeric endpoint remains
// available.
type RouteLease interface {
	Release(context.Context) error
}

type RouteLeaser interface {
	AcquireEndpointRoute(context.Context, netip.Addr) (RouteLease, error)
	Close() error
}

type PeerUpdate struct {
	PublicKey  string
	Endpoint   netip.AddrPort
	Generation uint64
}

// CoreControl is the only data-plane surface used by the management
// supervisor. Implementations may use a Unix socket, Windows named pipe, or an
// in-process test double.
type CoreControl interface {
	SetPeerEndpoint(context.Context, PeerUpdate) error
	WaitPeerReady(context.Context, PeerUpdate) error
	RedialPeer(context.Context, string) error
	Activate(context.Context) error
}

type PeerSpec struct {
	PublicKey string
	Endpoint  string
}

type NetworkChangeSource interface {
	Changes() <-chan struct{}
	Close() error
}
