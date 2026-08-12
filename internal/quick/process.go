package quick

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

type coreLaunch struct {
	ConfigPath     string
	Name           string
	Snapshot       []byte
	DeferEndpoints bool
	Debug          bool
}

func coreCommand(executable string, launch coreLaunch) (*exec.Cmd, error) {
	if len(launch.Snapshot) == 0 {
		return nil, errors.New("core configuration snapshot is required")
	}
	args := []string{
		"run", launch.ConfigPath,
		"--name", launch.Name,
		"--config-snapshot-stdin",
	}
	if launch.DeferEndpoints {
		args = append(args, "--defer-endpoints")
	}
	if launch.Debug {
		args = append(args, "--debug")
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdin = bytes.NewReader(launch.Snapshot)
	return cmd, nil
}

func coreExecutable() (string, error) {
	var validationErrors []error
	current, err := os.Executable()
	if err == nil {
		for _, candidate := range platformCoreExecutableCandidates(current) {
			if validationErr := platformValidateCoreExecutable(
				current,
				candidate,
			); validationErr == nil {
				return candidate, nil
			} else {
				validationErrors = append(validationErrors, fmt.Errorf(
					"candidate %q: %w", candidate, validationErr,
				))
			}
		}
	} else {
		validationErrors = append(validationErrors, fmt.Errorf(
			"resolve current wg-quic-quick executable: %w", err,
		))
	}
	path, err := platformCoreExecutableFallback()
	if err != nil {
		validationErrors = append(validationErrors, err)
		return "", fmt.Errorf(
			"cannot find a trusted wg-quic core executable next to wg-quic-quick: %w",
			errors.Join(validationErrors...),
		)
	}
	return path, nil
}
