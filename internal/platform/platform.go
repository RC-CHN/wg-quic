// Package platform isolates operating-system TUN and network lifecycle work
// from the shared WireGuard and transport core.
package platform

import (
	"context"

	"github.com/RC-CHN/wg-quic/internal/config"
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

// Host is the narrow capability boundary implemented by each operating
// system. Transport, FEC, obfuscation, WireGuard configuration, and device
// orchestration stay outside this interface and are shared by every port.
type Host interface {
	ValidateInterfaceName(string) error
	ControlPath(string) string
	Prepare(context.Context, *config.Config) error
	CreateTUN(name string, mtu int) (tun.Device, error)
	ConfigureNetwork(context.Context, string, *config.Config) (Cleanup, error)
	RunHook(context.Context, string, string) error
}
