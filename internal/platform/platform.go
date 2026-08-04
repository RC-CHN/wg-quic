// Package platform isolates operating-system TUN and network lifecycle work
// from the shared WireGuard and transport core.
package platform

import (
	"context"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
)

// Cleanup reverses network state installed by ConfigureNetwork.
type Cleanup func(context.Context) error

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
	ConfigPath(string) string
	Prepare(context.Context, *config.Config) error
	NewEndpointRouteLeaser(context.Context, string, *config.Config) (endpoint.RouteLeaser, error)
	ConfigureNetwork(context.Context, string, *config.Config) (Cleanup, error)
	RunHook(context.Context, string, string) error
}
