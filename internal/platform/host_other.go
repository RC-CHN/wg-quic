//go:build !linux && !freebsd && !windows

package platform

import (
	"context"
	"errors"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
)

var errHostNotImplemented = errors.New("wg-quic host integration is not implemented on this operating system")

type unsupportedHost struct{}

func Current() Host {
	return unsupportedHost{}
}

func (unsupportedHost) ValidateInterfaceName(string) error {
	return errHostNotImplemented
}

func (unsupportedHost) ControlPath(string) string {
	return ""
}

func (unsupportedHost) ConfigPath(string) string {
	return ""
}

func (unsupportedHost) Prepare(context.Context, *config.Config) error {
	return errHostNotImplemented
}

func (unsupportedHost) CreateTUN(string, int) (tun.Device, error) {
	return nil, errHostNotImplemented
}

func (unsupportedHost) ConfigureNetwork(context.Context, string, *config.Config) (Cleanup, error) {
	return nil, errHostNotImplemented
}

func (unsupportedHost) RunHook(context.Context, string, string) error {
	return errHostNotImplemented
}
