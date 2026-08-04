//go:build !linux && !freebsd && !windows

package quick

import "errors"

func newCoreProcess(coreLaunch) (coreProcess, error) {
	return nil, errors.New("wg-quic-quick core supervision is supported on Linux and FreeBSD")
}
