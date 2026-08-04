package main

import (
	"context"
	"errors"
	"fmt"
	"os"

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
	case "run", "debug":
		input, name, err := parseQuickRunArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := commandContext()
		defer stop()
		if args[0] == "debug" {
			return runQuickDebug(ctx, input, name)
		}
		return runQuick(ctx, input, name)
	case "check":
		if len(args) != 2 {
			return usage()
		}
		if err := quick.Check(args[1]); err != nil {
			return err
		}
		fmt.Println("configuration is valid for wg-quic-quick")
		return nil
	case "up", "down":
		if len(args) != 2 {
			return usage()
		}
		return quick.Manage(context.Background(), args[0], args[1])
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

func parseQuickRunArgs(args []string) (input, name string, err error) {
	if len(args) != 1 && len(args) != 3 {
		return "", "", errors.New("interface or configuration is required")
	}
	if len(args) == 3 {
		if args[1] != "--name" || args[2] == "" {
			return "", "", errors.New("invalid --name option")
		}
		name = args[2]
	}
	return args[0], name, nil
}

func usage() error {
	fmt.Fprintln(os.Stderr, `Usage:
  wg-quic-quick run INTERFACE|CONFIG [--name INTERFACE]
  wg-quic-quick debug INTERFACE|CONFIG [--name INTERFACE]
  wg-quic-quick check INTERFACE|CONFIG
  wg-quic-quick up INTERFACE
  wg-quic-quick down INTERFACE
  wg-quic-quick version`)
	return errors.New("invalid command line")
}
