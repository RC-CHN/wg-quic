//go:build !windows

package control

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func listen(path string) (net.Listener, func() error, error) {
	return listenUnix(path, 0o600)
}

func listenUnix(
	path string,
	mode os.FileMode,
) (net.Listener, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("refusing to replace non-socket path %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, nil, err
	}
	cleanup := func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return listener, cleanup, nil
}

func dial(path string, timeout time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: timeout}
	return dialer.Dial("unix", path)
}

func listenReadOnlyStatus(
	path string,
) (net.Listener, func() error, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, nil, err
	}
	// The endpoint itself is status-only, so its parent must also be
	// traversable by unprivileged local users. A restrictive service umask
	// otherwise turns the world-readable socket into an unusable endpoint.
	if err := os.Chmod(directory, 0o755); err != nil {
		return nil, nil, err
	}
	return listenUnix(readOnlyStatusPath(path), 0o666)
}

func readOnlyStatusPath(path string) string {
	return path + ".status"
}
