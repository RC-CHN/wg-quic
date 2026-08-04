//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
)

var windowsInterfaceNamePattern = regexp.MustCompile(`^[^\\/:*?"<>|\x00-\x1f]{1,128}$`)

type windowsHost struct{}

type windowsNetworkState struct {
	undo []string
}

func Current() Host {
	return windowsHost{}
}

func (windowsHost) ValidateInterfaceName(name string) error {
	if !windowsInterfaceNamePattern.MatchString(name) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("invalid Windows interface name %q", name)
	}
	return nil
}

func (windowsHost) ControlPath(name string) string {
	return `\\.\pipe\wg-quic-` + name
}

func (windowsHost) ConfigPath(name string) string {
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	return filepath.Join(root, "wg-quic", "interfaces", name+".conf")
}

func (windowsHost) Prepare(context.Context, *config.Config) error {
	return nil
}

func (windowsHost) CreateTUN(name string, mtu int) (tun.Device, error) {
	tun.WintunTunnelType = "wg-quic"
	return tun.CreateTUN(name, mtu)
}

func (windowsHost) ConfigureNetwork(ctx context.Context, name string, cfg *config.Config) (Cleanup, error) {
	endpoints, err := windowsEndpointAddresses(ctx, cfg)
	if err != nil {
		return nil, err
	}
	operations, err := windowsNetworkOperations(name, cfg, endpoints)
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
