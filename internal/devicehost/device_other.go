//go:build !linux && !freebsd && !windows

package devicehost

import (
	"github.com/RC-CHN/wg-quic/internal/platformenv"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
)

func (systemHost) CreateTUN(string, int) (tun.Device, error) {
	return nil, platformenv.ErrUnsupported
}
