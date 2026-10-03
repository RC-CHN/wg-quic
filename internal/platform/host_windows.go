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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	luid, err := windowsInterfaceLUID(name)
	if err != nil {
		return nil, err
	}
	system := windowsNativeNetworkSystem{}
	state := &windowsNetworkState{
		name: name, interfaceLUID: luid,
		compartmentID: system.CurrentCompartmentID(),
	}
	cleanup := func(cleanupCtx context.Context) error {
		return state.rollback(cleanupCtx, system)
	}
	return cleanup, state.apply(ctx, operations, system)
}

func (windowsHost) RunHook(ctx context.Context, hook, name string) error {
	hook = strings.ReplaceAll(hook, "%i", name)
	cmd := exec.CommandContext(ctx, "cmd.exe", "/D", "/S", "/C", hook)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
