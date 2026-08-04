//go:build !linux && !freebsd

package quick

import (
	"context"
	"errors"
)

var errManagementNotImplemented = errors.New("wg-quic-quick is supported on Linux and FreeBSD; Windows will use the service control manager")

func Manage(context.Context, string, string) error {
	return errManagementNotImplemented
}
