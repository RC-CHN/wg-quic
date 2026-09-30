//go:build windows

package platform

import (
	"context"
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
		scripts := make([]string, 0, len(state.undo))
		for i := len(state.undo) - 1; i >= 0; i-- {
			scripts = append(scripts, state.undo[i])
		}
		_, err := runWindowsNetworkBatch(cleanupCtx, name, scripts, true)
		return err
	}
	scripts := make([]string, len(operations))
	for i, operation := range operations {
		scripts[i] = operation.apply
	}
	completed, applyErr := runWindowsNetworkBatch(ctx, name, scripts, false)
	for _, i := range completed {
		if operations[i].undo != "" {
			state.undo = append(state.undo, operations[i].undo)
		}
	}
	if applyErr != nil {
		return cleanup, applyErr
	}

	return cleanup, nil
}

func (windowsHost) RunHook(ctx context.Context, hook, name string) error {
	hook = strings.ReplaceAll(hook, "%i", name)
	cmd := exec.CommandContext(ctx, "cmd.exe", "/D", "/S", "/C", hook)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
