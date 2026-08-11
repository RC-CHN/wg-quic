//go:build !windows

package quick

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

const maxDesktopConfigSize = 1024 * 1024

// ImportDesktopConfig installs one validated configuration. Desktop shells
// execute this narrow operation through pkexec instead of running their whole
// webview process with root privileges.
func ImportDesktopConfig(
	source string,
	name string,
	overwrite bool,
) error {
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return err
	}
	if err := Check(source); err != nil {
		return err
	}
	return importUnixDesktopConfig(
		source,
		host.ConfigPath(name),
		overwrite,
	)
}

func importUnixDesktopConfig(
	source string,
	destination string,
	overwrite bool,
) error {
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve desktop import source: %w", err)
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve desktop import destination: %w", err)
	}
	if sourcePath == destinationPath {
		return errors.New("desktop import source and destination are identical")
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect desktop import source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("desktop import source must be a regular file")
	}
	if info.Size() > maxDesktopConfigSize {
		return fmt.Errorf(
			"desktop import source is %d bytes; maximum is %d",
			info.Size(),
			maxDesktopConfigSize,
		)
	}

	directory := filepath.Dir(destinationPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create desktop configuration directory: %w", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		return fmt.Errorf("make desktop configurations discoverable: %w", err)
	}
	input, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open desktop import source: %w", err)
	}
	defer input.Close()

	output, err := os.CreateTemp(
		directory,
		"."+filepath.Base(destinationPath)+".tmp-",
	)
	if err != nil {
		return fmt.Errorf("create temporary desktop configuration: %w", err)
	}
	temporaryPath := output.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := output.Chmod(0o600); err != nil {
		output.Close()
		return fmt.Errorf("restrict temporary desktop configuration: %w", err)
	}
	copied, err := io.Copy(output, io.LimitReader(
		input,
		maxDesktopConfigSize+1,
	))
	if err != nil {
		output.Close()
		return fmt.Errorf("copy desktop configuration: %w", err)
	}
	if copied > maxDesktopConfigSize {
		output.Close()
		return fmt.Errorf(
			"desktop import source exceeded %d bytes while copying",
			maxDesktopConfigSize,
		)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return fmt.Errorf("flush desktop configuration: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close desktop configuration: %w", err)
	}

	if overwrite {
		if err := os.Rename(temporaryPath, destinationPath); err != nil {
			return fmt.Errorf("replace desktop configuration: %w", err)
		}
	} else {
		if err := os.Link(temporaryPath, destinationPath); err != nil {
			if errors.Is(err, os.ErrExist) || strings.Contains(
				strings.ToLower(err.Error()),
				"file exists",
			) {
				return os.ErrExist
			}
			return fmt.Errorf("install desktop configuration: %w", err)
		}
		if err := os.Remove(temporaryPath); err != nil {
			return fmt.Errorf("finalize desktop configuration: %w", err)
		}
	}
	keepTemporary = false
	return nil
}
