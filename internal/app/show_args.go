package app

import (
	"errors"
	"strings"
)

func ParseShowArgs(args []string) (name string, jsonOutput bool, err error) {
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else if strings.HasPrefix(arg, "-") || name != "" {
			return "", false, errors.New("show accepts at most one interface and --json")
		} else {
			name = arg
		}
	}
	return name, jsonOutput, nil
}
