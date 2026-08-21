//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/internal/platformenv"
)

type windowsHost struct {
	platformenv.Paths
}

type windowsNetworkState struct {
	undo []string
}

func Current() Host {
	return windowsHost{}
}

func (windowsHost) Prepare(context.Context, *config.Config) error {
	return nil
}

func (windowsHost) NewEndpointRouteLeaser(
	_ context.Context,
	name string,
	_ *config.Config,
) (endpoint.RouteLeaser, error) {
	return newWindowsRouteManager(name)
}

func (windowsHost) NewPeerRouteManager(
	ctx context.Context,
	name string,
	cfg *config.Config,
) (PeerRouteManager, error) {
	return newWindowsPeerRouteManager(ctx, name, cfg)
}

func (windowsHost) ConfigureNetwork(ctx context.Context, name string, cfg *config.Config) (Cleanup, error) {
	operations, err := windowsNetworkOperations(name, cfg)
	if err != nil {
		return nil, err
	}
	state := &windowsNetworkState{}
	cleanup := func(cleanupCtx context.Context) error {
		var errs []error
		for i := len(state.undo) - 1; i >= 0; i-- {
			if err := runWindowsPowerShell(cleanupCtx, state.undo[i]); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	for _, operation := range operations {
		if err := runWindowsPowerShell(ctx, operation.apply); err != nil {
			return cleanup, err
		}
		if operation.undo != "" {
			state.undo = append(state.undo, operation.undo)
		}
	}
	return cleanup, nil
}

func (windowsHost) RunHook(ctx context.Context, hook, name string) error {
	hook = strings.ReplaceAll(hook, "%i", name)
	cmd := exec.CommandContext(ctx, "cmd.exe", "/D", "/S", "/C", hook)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func runWindowsPowerShell(ctx context.Context, script string) error {
	cmd := exec.CommandContext(
		ctx,
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", script,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("PowerShell network configuration: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
