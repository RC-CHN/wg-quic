//go:build linux || freebsd

package devicehost

import "github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"

func (systemHost) CreateTUN(name string, mtu int) (tun.Device, error) {
	return tun.CreateTUN(name, mtu)
}
