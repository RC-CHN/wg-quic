package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/RC-CHN/wg-quic/internal/app"
	"github.com/RC-CHN/wg-quic/internal/config"
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
		if len(args) < 2 || len(args) > 4 {
			return usage()
		}
		name := ""
		if len(args) == 4 {
			if args[2] != "--name" {
				return usage()
			}
			name = args[3]
		}
		return app.Run(context.Background(), args[1], name)
	case "check":
		if len(args) != 2 {
			return usage()
		}
		if _, err := config.ParseFile(args[1]); err != nil {
			return err
		}
		fmt.Println("configuration is valid")
		return nil
	case "up", "down":
		if len(args) != 2 {
			return usage()
		}
		return app.Manage(context.Background(), args[0], args[1])
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
  wg-quic run CONFIG [--name INTERFACE]
  wg-quic check CONFIG
  wg-quic up INTERFACE
  wg-quic down INTERFACE
  wg-quic show [INTERFACE] [--json]
  wg-quic genkey
  wg-quic pubkey
  wg-quic version`)
	return errors.New("invalid command line")
}
