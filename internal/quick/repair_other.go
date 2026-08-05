//go:build !windows

package quick

import (
	"context"
	"errors"
)

func Repair(context.Context, string) (RepairResult, error) {
	return RepairResult{}, errors.New("wg-quic-quick down --repair is supported only on Windows")
}
