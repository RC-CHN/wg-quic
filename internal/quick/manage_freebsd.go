//go:build freebsd

package quick

import (
	"context"
	"fmt"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

func Manage(ctx context.Context, action, name string) error {
	if err := platform.Current().ValidateInterfaceName(name); err != nil {
		return err
	}
	commandName, args, err := serviceCommand(action, name)
	if err != nil {
		return err
	}
	return command(ctx, commandName, args...)
}

func serviceCommand(action, name string) (string, []string, error) {
	switch action {
	case "up":
		return "service", []string{"wg_quic", "onestart", name}, nil
	case "down":
		return "service", []string{"wg_quic", "onestop", name}, nil
	default:
		return "", nil, fmt.Errorf("unknown management action %q", action)
	}
}
