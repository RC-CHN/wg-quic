//go:build !linux && !freebsd && !windows

package quick

import "errors"

func newCoreProcess(string, string, uint32, bool) (coreProcess, error) {
	return nil, errors.New("wg-quic-quick core supervision is supported on Linux and FreeBSD")
}
