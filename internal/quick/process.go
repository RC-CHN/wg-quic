package quick

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	current, err := os.Executable()
	if err == nil {
		sibling := filepath.Join(filepath.Dir(current), coreExecutableName())
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			return sibling, nil
		}
	}
	path, err := exec.LookPath(coreExecutableName())
	if err != nil {
		return "", errors.New("cannot find the wg-quic core executable next to wg-quic-quick or in PATH")
	}
	return path, nil
}
