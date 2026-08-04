//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/RC-CHN/wg-quic/internal/quick"
)

func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runQuick(ctx context.Context, input, name string) error {
	return quick.Run(ctx, input, name)
}
