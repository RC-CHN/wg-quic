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
		if len(args) < 2 {
			return usage()
		}
		options := core.RunOptions{}
		for i := 2; i < len(args); i += 2 {
			if i+1 >= len(args) {
				return usage()
			}
			switch args[i] {
			case "--name":
				options.Name = args[i+1]
			case "--fwmark":
				value, err := strconv.ParseUint(args[i+1], 0, 32)
				if err != nil {
					return fmt.Errorf("invalid --fwmark: %w", err)
				}
				mark := uint32(value)
				options.FwMark = &mark
			default:
				return usage()
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return core.Run(ctx, args[1], options)
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

func usage() error {
	fmt.Fprintln(os.Stderr, `Usage:
  wg-quic run CONFIG [--name INTERFACE] [--fwmark MARK]
  wg-quic check CONFIG
  wg-quic show [INTERFACE] [--json]
  wg-quic genkey
  wg-quic pubkey
  wg-quic version`)
	return errors.New("invalid command line")
}
