package wgdevice

import (
	"github.com/RC-CHN/wg-quic/internal/config"
	"golang.zx2c4.com/wireguard/device"
)

func Configure(dev *device.Device, cfg *config.Config) error {
	uapi, err := cfg.UAPI()
	if err != nil {
		return err
	}
	return dev.IpcSet(uapi)
}
