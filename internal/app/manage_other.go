//go:build !linux

package app

import (
	"context"
	"errors"
)

var errManagementNotImplemented = errors.New("wg-quic service management is not implemented on this operating system")

func Manage(context.Context, string, string) error {
	return errManagementNotImplemented
}
