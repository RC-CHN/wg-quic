// Package platform isolates operating-system TUN and network lifecycle work
// from the shared WireGuard and transport core.
package platform

import (
	"context"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
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

// DeviceHost is the only operating-system surface required by the shared
// userspace data plane. It deliberately excludes addresses, routes, DNS, and
// hooks so the core daemon can run without owning wg-quick policy.
type DeviceHost interface {
	ValidateInterfaceName(string) error
	ControlPath(string) string
	CreateTUN(name string, mtu int) (tun.Device, error)
}

// Host extends DeviceHost with the policy operations used by
// wg-quic-quick. Linux and FreeBSD implement this interface; future desktop
// clients should control an OS service that exposes the same core lifecycle
// instead of performing these privileged operations in the UI process.
type Host interface {
	DeviceHost
	ConfigPath(string) string
	Prepare(context.Context, *config.Config) error
	NewEndpointRouteLeaser(context.Context, string, *config.Config) (endpoint.RouteLeaser, error)
	ConfigureNetwork(context.Context, string, *config.Config) (Cleanup, error)
	RunHook(context.Context, string, string) error
}
