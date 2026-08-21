//go:build !linux && !freebsd && !windows

package quick

import (
	"errors"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func openSecureConfigSnapshot(string) (*config.Config, error) {
	return nil, errors.New("secure runtime configuration snapshots are unsupported on this platform")
}
