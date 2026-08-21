// Package platform isolates operating-system TUN and network lifecycle work
// from the shared WireGuard and transport core.
package platform

import (
	"context"
	"net/netip"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
)

// Cleanup reverses network state installed by ConfigureNetwork.
type Cleanup func(context.Context) error

type PeerRoutePlan struct {
	TransactionID string
	Additions     []netip.Prefix
	Removals      []netip.Prefix
}

type RecoveryStatus struct {
	State                    string
	RetainedAmbiguousObjects int
	Message                  string
}

type RecoveryStatusProvider interface {
	RecoveryStatus() RecoveryStatus
}

type PreparedPeerRoutes interface {
	CommitRemovals(context.Context) error
	CommitAdditions(context.Context) error
	RollbackAdditions(context.Context) error
	RollbackRemovals(context.Context) error
	Rollback(context.Context) error
	Finalize(context.Context) error
}

type PeerRouteManager interface {
	Prepare(context.Context, PeerRoutePlan) (PreparedPeerRoutes, error)
}

type hostCommand struct {
	name string
	args []string
}

type hostOperation struct {
	apply hostCommand
	undo  hostCommand
}

// Host exposes only the policy operations used by wg-quic-quick. Device
// creation lives in package devicehost, so management clients do not compile
// the WireGuard TUN implementation. Future desktop
// clients should control an OS service that exposes the same core lifecycle
// instead of performing these privileged operations in the UI process.
type Host interface {
	ValidateInterfaceName(string) error
	ControlPath(string) string
	ManagementPath(string) string
	ConfigPath(string) string
	Prepare(context.Context, *config.Config) error
	NewEndpointRouteLeaser(context.Context, string, *config.Config) (endpoint.RouteLeaser, error)
	NewPeerRouteManager(context.Context, string, *config.Config) (PeerRouteManager, error)
	ConfigureNetwork(context.Context, string, *config.Config) (Cleanup, error)
	RunHook(context.Context, string, string) error
}
