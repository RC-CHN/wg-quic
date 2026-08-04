//go:build linux

package app

import (
	"context"
	"fmt"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

func Manage(ctx context.Context, action, name string) error {
	if err := platform.Current().ValidateInterfaceName(name); err != nil {
		return err
	}
	switch action {
	case "up":
		return command(ctx, "systemctl", "start", "wg-quic@"+name+".service")
	case "down":
		return command(ctx, "systemctl", "stop", "wg-quic@"+name+".service")
	default:
		return fmt.Errorf("unknown management action %q", action)
	}
}
