//go:build !linux

package device

import (
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/conn"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/rwcancel"
)

func (device *Device) startRouteListener(_ conn.Bind) (*rwcancel.RWCancel, error) {
	return nil, nil
}
