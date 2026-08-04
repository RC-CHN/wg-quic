// Package devicehost is the only operating-system capability surface used by
// the portable userspace data plane.
package devicehost

import (
	"github.com/RC-CHN/wg-quic/internal/platformenv"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
)

type Host interface {
	ValidateInterfaceName(string) error
	ControlPath(string) string
	CreateTUN(name string, mtu int) (tun.Device, error)
}

type systemHost struct {
	platformenv.Paths
}

func Current() Host {
	return systemHost{}
}
