//go:build linux

package quick

import (
	"context"
	"fmt"
	"os"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

const (
	openWrtInitScript  = "/etc/init.d/wg-quic"
	openWrtReleaseFile = "/etc/openwrt_release"
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
	if _, err := os.Stat(openWrtReleaseFile); err != nil {
		return linuxServiceCommand(action, name, false, false)
	}
	info, initErr := os.Stat(openWrtInitScript)
	initExecutable := initErr == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
	return linuxServiceCommand(action, name, true, initExecutable)
}

func linuxServiceCommand(action, name string, openWrt, initExecutable bool) (string, []string, error) {
	if !openWrt {
		return systemdServiceCommand(action, name)
	}
	if !initExecutable {
		return "", nil, fmt.Errorf(
			"OpenWrt service %s is missing or not executable; install the wg-quic OpenWrt package or use wg-quic-quick run",
			openWrtInitScript,
		)
	}
	return openWrtServiceCommand(action, name)
}

func systemdServiceCommand(action, name string) (string, []string, error) {
	switch action {
	case "up":
		return "systemctl", []string{"start", "wg-quic@" + name + ".service"}, nil
	case "down":
		return "systemctl", []string{"stop", "wg-quic@" + name + ".service"}, nil
	default:
		return "", nil, fmt.Errorf("unknown management action %q", action)
	}
}

func openWrtServiceCommand(action, name string) (string, []string, error) {
	switch action {
	case "up":
		return openWrtInitScript, []string{"start", name}, nil
	case "down":
		return openWrtInitScript, []string{"stop", name}, nil
	default:
		return "", nil, fmt.Errorf("unknown management action %q", action)
	}
}
