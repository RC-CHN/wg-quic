//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/RC-CHN/wg-quic/internal/quick"
)

func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runQuick(
	ctx context.Context,
	input,
	name string,
	brokerSafe bool,
) error {
	if brokerSafe {
		return errors.New("--broker-safe is only supported by an installed Windows service")
	}
	return quick.Run(ctx, input, name)
}

func runQuickDebug(context.Context, string, string) error {
	return errors.New("wg-quic-quick debug is currently supported on Windows")
}

func runDesktopHelper(context.Context, string) error {
	return errors.New("wg-quic-quick desktop helper is only supported on Windows")
}

func runDesktopClient(
	context.Context,
	desktopClientRequest,
) (string, error) {
	return "", errors.New("wg-quic-quick desktop client is only supported on Windows")
}

func runManagementService() error {
	return errors.New("wg-quic management service is only supported on Windows")
}

func runDesktopBrokerStatus(context.Context) (string, error) {
	return "", errors.New("wg-quic desktop broker status is only supported on Windows")
}
