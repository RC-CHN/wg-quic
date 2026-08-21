//go:build !windows

package management

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func listen(path string) (net.Listener, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("refusing to replace non-socket management path %s", path)
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
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
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

func dial(ctx context.Context, path string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}
