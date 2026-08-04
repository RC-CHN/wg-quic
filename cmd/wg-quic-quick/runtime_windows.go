//go:build windows

package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/RC-CHN/wg-quic/internal/quick"
	"golang.org/x/sys/windows/svc"
)

func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func runQuick(ctx context.Context, input, name string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if isService {
		return quick.RunWindowsService(input, name)
	}
	return quick.Run(ctx, input, name)
}
