//go:build linux || freebsd

package quick

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"golang.org/x/sys/unix"
)

func openSecureConfigSnapshot(path string) (*config.Config, error) {
	return openSecureUnixConfigSnapshot(path, string(filepath.Separator), 0)
}

// openSecureUnixConfigSnapshot walks from a pinned trusted-root descriptor.
// Every child is opened relative to its pinned parent with O_NOFOLLOW, so a
// pathname swap cannot redirect parsing after validation.
func openSecureUnixConfigSnapshot(
	path string,
	trustedRoot string,
	expectedOwner uint32,
) (*config.Config, error) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(trustedRoot) {
		return nil, errors.New("secure configuration and trusted-root paths must be absolute")
	}
	path = filepath.Clean(path)
	trustedRoot = filepath.Clean(trustedRoot)
	relative, err := filepath.Rel(trustedRoot, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("configuration %q is outside trusted root %q", path, trustedRoot)
	}
	components := strings.Split(relative, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("configuration path contains unsafe component %q", component)
		}
	}

	rootFD, err := unix.Open(
		trustedRoot,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open trusted configuration root %q: %w", trustedRoot, err)
	}
	currentFD := rootFD
	defer func() { _ = unix.Close(currentFD) }()
	if err := validateSecureUnixDescriptor(currentFD, trustedRoot, expectedOwner, true); err != nil {
		return nil, err
	}

	currentPath := trustedRoot
	for _, component := range components[:len(components)-1] {
		nextFD, openErr := unix.Openat(
			currentFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		if openErr != nil {
			return nil, fmt.Errorf("open secure configuration directory %q: %w", component, openErr)
		}
		_ = unix.Close(currentFD)
		currentFD = nextFD
		currentPath = filepath.Join(currentPath, component)
		if err := validateSecureUnixDescriptor(currentFD, currentPath, expectedOwner, true); err != nil {
			return nil, err
		}
	}

	name := components[len(components)-1]
	fileFD, err := unix.Openat(
		currentFD,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open secure configuration file %q: %w", path, err)
	}
	file := os.NewFile(uintptr(fileFD), path)
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, errors.New("adopt secure configuration descriptor")
	}
	defer file.Close()
	if err := validateSecureUnixDescriptor(fileFD, path, expectedOwner, false); err != nil {
		return nil, err
	}
	return parseSecureConfigReader(file)
}

func validateSecureUnixDescriptor(
	fd int,
	path string,
	expectedOwner uint32,
	directory bool,
) error {
	var status unix.Stat_t
	if err := unix.Fstat(fd, &status); err != nil {
		return fmt.Errorf("inspect secure configuration path %q: %w", path, err)
	}
	wantType := uint32(unix.S_IFREG)
	kind := "regular file"
	if directory {
		wantType = uint32(unix.S_IFDIR)
		kind = "directory"
	}
	if uint32(status.Mode)&uint32(unix.S_IFMT) != wantType {
		return fmt.Errorf("secure configuration path %q is not a %s", path, kind)
	}
	if status.Uid != expectedOwner {
		return fmt.Errorf(
			"secure configuration path %q is owned by uid %d; want uid %d",
			path, status.Uid, expectedOwner,
		)
	}
	if uint32(status.Mode)&0o022 != 0 {
		return fmt.Errorf("secure configuration path %q is group/world writable", path)
	}
	if !directory && status.Nlink != 1 {
		return fmt.Errorf(
			"secure configuration file %q has %d hard links; want exactly one",
			path, status.Nlink,
		)
	}
	return nil
}
