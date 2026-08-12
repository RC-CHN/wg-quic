package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/RC-CHN/wg-quic/internal/app"
	"github.com/RC-CHN/wg-quic/internal/quick"
)

var version = "0.1.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wg-quic-quick:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "broker-service":
		if len(args) != 1 {
			return usage()
		}
		return runManagementService()
	case "desktop-broker-status":
		if len(args) != 1 {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		status, err := runDesktopBrokerStatus(ctx)
		if err != nil {
			return err
		}
		fmt.Println(status)
		return nil
	case "desktop-import":
		name, source, overwrite, err := parseDesktopImportArgs(args[1:])
		if err != nil {
			return usage()
		}
		return quick.ImportDesktopConfig(source, name, overwrite)
	case "desktop-client":
		request, err := parseDesktopClientArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		message, err := runDesktopClient(ctx, request)
		if err != nil {
			return err
		}
		if message != "" {
			fmt.Println(message)
		}
		return nil
	case "desktop-helper":
		pipePath, err := parseDesktopHelperArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		return runDesktopHelper(ctx, pipePath)
	case "run", "debug":
		input, name, brokerSafe, err := parseQuickRunArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		if args[0] == "debug" {
			if brokerSafe {
				return usage()
			}
			return runQuickDebug(ctx, input, name)
		}
		return runQuick(ctx, input, name, brokerSafe)
	case "check":
		if len(args) != 2 {
			return usage()
		}
		if err := quick.Check(args[1]); err != nil {
			return err
		}
		fmt.Println("configuration is valid for wg-quic-quick")
		return nil
	case "show":
		name, jsonOutput, err := app.ParseShowArgs(args[1:])
		if err != nil {
			return err
		}
		return app.Show(name, jsonOutput)
	case "up":
		if len(args) != 2 {
			return usage()
		}
		return quick.Manage(context.Background(), args[0], args[1])
	case "down":
		name, repair, err := parseQuickDownArgs(args[1:])
		if err != nil {
			return usage()
		}
		if repair {
			result, err := quick.Repair(context.Background(), name)
			if err != nil {
				return err
			}
			if result.ForcedServiceTermination {
				fmt.Printf(
					"repair completed for %s; the stuck service process was explicitly terminated\n",
					name,
				)
			} else {
				fmt.Printf("repair completed for %s\n", name)
			}
			return nil
		}
		return quick.Manage(context.Background(), args[0], name)
	case "version", "--version":
		if len(args) != 1 {
			return usage()
		}
		fmt.Printf("wg-quic-quick %s\n", version)
		return nil
	default:
		return usage()
	}
}

func parseDesktopHelperArgs(args []string) (string, error) {
	if len(args) != 1 || args[0] == "" {
		return "", errors.New("desktop helper pipe is required")
	}
	return args[0], nil
}

func parseDesktopImportArgs(
	args []string,
) (name string, source string, overwrite bool, err error) {
	switch {
	case len(args) == 2 && args[0] != "" && args[1] != "":
		return args[0], args[1], false, nil
	case len(args) == 3 &&
		args[0] != "" &&
		args[1] != "" &&
		args[2] == "--overwrite":
		return args[0], args[1], true, nil
	default:
		return "", "", false, errors.New("invalid desktop import arguments")
	}
}

type desktopClientRequest struct {
	action    string
	name      string
	source    string
	overwrite bool
}

func parseDesktopClientArgs(args []string) (desktopClientRequest, error) {
	if len(args) < 2 {
		return desktopClientRequest{}, errors.New("desktop action and interface are required")
	}
	request := desktopClientRequest{action: args[0], name: args[1]}
	switch request.action {
	case "up", "down", "check":
		if len(args) != 2 {
			return desktopClientRequest{}, errors.New("desktop action received unexpected arguments")
		}
	case "import":
		switch {
		case len(args) == 3:
			request.source = args[2]
		case len(args) == 4 && args[3] == "--overwrite":
			request.source = args[2]
			request.overwrite = true
		default:
			return desktopClientRequest{}, errors.New("invalid desktop import arguments")
		}
	default:
		return desktopClientRequest{}, fmt.Errorf("unsupported desktop action %q", request.action)
	}
	if request.name == "" {
		return desktopClientRequest{}, errors.New("desktop interface is required")
	}
	return request, nil
}

func parseQuickRunArgs(
	args []string,
) (input, name string, brokerSafe bool, err error) {
	if len(args) == 0 || args[0] == "" {
		return "", "", false, errors.New(
			"interface or configuration is required",
		)
	}
	input = args[0]
	remaining := args[1:]
	if len(remaining) >= 2 && remaining[0] == "--name" {
		if remaining[1] == "" {
			return "", "", false, errors.New("invalid --name option")
		}
		name = remaining[1]
		remaining = remaining[2:]
	}
	if len(remaining) == 1 && remaining[0] == "--broker-safe" {
		brokerSafe = true
		remaining = nil
	}
	if len(remaining) != 0 {
		return "", "", false, errors.New("invalid run options")
	}
	return input, name, brokerSafe, nil
}

func parseQuickDownArgs(args []string) (name string, repair bool, err error) {
	switch {
	case len(args) == 1 && args[0] != "":
		return args[0], false, nil
	case len(args) == 2 && args[0] != "" && args[1] == "--repair":
		return args[0], true, nil
	default:
		return "", false, errors.New("invalid down arguments")
	}
}

func usage() error {
	fmt.Fprintln(os.Stderr, `Usage:
  wg-quic-quick run INTERFACE|CONFIG [--name INTERFACE]
  wg-quic-quick debug INTERFACE|CONFIG [--name INTERFACE]
  wg-quic-quick check INTERFACE|CONFIG
  wg-quic-quick show [INTERFACE] [--json]
  wg-quic-quick up INTERFACE
  wg-quic-quick down INTERFACE [--repair]
  wg-quic-quick version`)
	return errors.New("invalid command line")
}
