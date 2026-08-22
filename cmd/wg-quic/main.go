package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/RC-CHN/wg-quic/internal/app"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/core"
)

var version = "0.1.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wg-quic:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "run":
		configPath, options, err := parseRunArgs(args[1:])
		if err != nil {
			return usage()
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return core.Run(ctx, configPath, options)
	case "check":
		if len(args) != 2 {
			return usage()
		}
		if _, err := config.ParseFile(args[1]); err != nil {
			return err
		}
		fmt.Println("configuration is valid")
		return nil
	case "show":
		name, jsonOutput, err := app.ParseShowArgs(args[1:])
		if err != nil {
			return err
		}
		return app.Show(name, jsonOutput)
	case "genkey":
		if len(args) != 1 {
			return usage()
		}
		key, err := app.GeneratePrivateKey()
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	case "pubkey":
		if len(args) != 1 {
			return usage()
		}
		key, err := app.PubkeyFromReader(os.Stdin)
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	case "version", "--version":
		if len(args) != 1 {
			return usage()
		}
		fmt.Printf("wg-quic %s\n", version)
		return nil
	default:
		return usage()
	}
}

func parseRunArgs(args []string) (string, core.RunOptions, error) {
	if len(args) == 0 {
		return "", core.RunOptions{}, errors.New("configuration is required")
	}
	configPath := args[0]
	options := core.RunOptions{}
	for i := 1; i < len(args); {
		switch args[i] {
		case "--debug":
			options.Debug = true
			i++
		case "--defer-endpoints":
			options.DeferEndpoints = true
			i++
		case "--config-snapshot-stdin":
			if options.Snapshot != nil {
				return "", core.RunOptions{}, errors.New("--config-snapshot-stdin was specified more than once")
			}
			options.Snapshot = os.Stdin
			i++
		case "--name", "--fwmark", "--supervisor-fd":
			if i+1 >= len(args) {
				return "", core.RunOptions{}, fmt.Errorf("%s requires a value", args[i])
			}
			switch args[i] {
			case "--name":
				options.Name = args[i+1]
			case "--fwmark":
				value, err := strconv.ParseUint(args[i+1], 0, 32)
				if err != nil {
					return "", core.RunOptions{}, fmt.Errorf("invalid --fwmark: %w", err)
				}
				mark := uint32(value)
				options.FwMark = &mark
			case "--supervisor-fd":
				value, err := strconv.ParseUint(args[i+1], 10, 31)
				if err != nil || value < 3 {
					return "", core.RunOptions{}, errors.New("invalid --supervisor-fd")
				}
				if options.SupervisorFD != 0 {
					return "", core.RunOptions{}, errors.New("--supervisor-fd was specified more than once")
				}
				options.SupervisorFD = uintptr(value)
			}
			i += 2
		default:
			return "", core.RunOptions{}, fmt.Errorf("unknown option %q", args[i])
		}
	}
	if options.SupervisorFD != 0 && options.Snapshot == nil {
		return "", core.RunOptions{}, errors.New("--supervisor-fd requires --config-snapshot-stdin")
	}
	return configPath, options, nil
}

func usage() error {
	fmt.Fprintln(os.Stderr, `Usage:
  wg-quic run CONFIG [--name INTERFACE] [--fwmark MARK] [--debug] [--defer-endpoints]
  wg-quic check CONFIG
  wg-quic show [INTERFACE] [--json]
  wg-quic genkey
  wg-quic pubkey
  wg-quic version`)
	return errors.New("invalid command line")
}
