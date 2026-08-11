//go:build windows

package quick

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

const (
	maxWindowsDesktopConfigSize   = 1024 * 1024
	maxWindowsDesktopEnvelopeSize = 2*maxWindowsDesktopConfigSize + 64*1024
)

// RunWindowsDesktopHelper is the deliberately narrow elevated boundary used
// by the desktop UI. Only a random local pipe name crosses the UAC command-line
// boundary. The validated operation request and its result travel over that
// same duplex pipe, so elevation never depends on inheriting caller-specific
// environment variables.
func RunWindowsDesktopHelper(ctx context.Context, pipePath string) error {
	connection, err := openWindowsDesktopPipe(pipePath)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(
		windowsDesktopOperationTimeout + windowsDesktopResultGrace,
	))

	request, operationErr := readWindowsDesktopRequest(connection)
	if operationErr == nil {
		var deadline time.Time
		deadline, operationErr = windowsDesktopRequestDeadline(
			request, time.Now(),
		)
		if operationErr == nil {
			operationErr = runWindowsDesktopHelperUntilDeadline(
				ctx, request, deadline, runWindowsDesktopHelper,
			)
		}
	}
	message := ""
	if operationErr != nil {
		message = operationErr.Error()
	} else if request.Action == "check" {
		message = "configuration is valid for wg-quic-quick"
	}
	resultErr := json.NewEncoder(connection).Encode(struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}{
		Success: operationErr == nil,
		Message: message,
	})
	if resultErr != nil {
		resultErr = fmt.Errorf("report desktop helper result: %w", resultErr)
	}
	return errors.Join(operationErr, resultErr)
}

func readWindowsDesktopRequest(
	connection io.Reader,
) (windowsDesktopRequest, error) {
	limited := &io.LimitedReader{
		R: connection,
		N: maxWindowsDesktopEnvelopeSize + 1,
	}
	var request windowsDesktopRequest
	if err := json.NewDecoder(limited).Decode(&request); err != nil {
		return windowsDesktopRequest{}, fmt.Errorf(
			"decode desktop helper request: %w", err,
		)
	}
	if limited.N == 0 {
		return windowsDesktopRequest{}, errors.New(
			"desktop helper request exceeded its size limit",
		)
	}
	return request, nil
}

func windowsDesktopRequestDeadline(
	request windowsDesktopRequest,
	now time.Time,
) (time.Time, error) {
	if request.DeadlineUnixMillis <= 0 {
		return time.Time{}, errors.New(
			"desktop helper request deadline is required",
		)
	}
	deadline := time.UnixMilli(request.DeadlineUnixMillis)
	maximum := now.Add(windowsDesktopOperationTimeout)
	if deadline.After(maximum) {
		deadline = maximum
	}
	if !deadline.After(now) {
		return time.Time{}, errors.New(
			"elevated desktop operation deadline expired before the helper received its request",
		)
	}
	return deadline, nil
}

type windowsDesktopOperation func(
	context.Context,
	windowsDesktopRequest,
) error

func runWindowsDesktopHelperUntilDeadline(
	ctx context.Context,
	request windowsDesktopRequest,
	deadline time.Time,
	operation windowsDesktopOperation,
) error {
	operationContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- operation(operationContext, request)
	}()
	select {
	case err := <-result:
		if contextErr := operationContext.Err(); contextErr != nil {
			return windowsDesktopOperationEndError(contextErr)
		}
		return windowsDesktopOperationEndError(err)
	case <-operationContext.Done():
		return windowsDesktopOperationEndError(operationContext.Err())
	}
}

func windowsDesktopOperationEndError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf(
			"elevated desktop operation deadline expired: %w", err,
		)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("elevated desktop operation canceled: %w", err)
	default:
		return err
	}
}

func runWindowsDesktopHelper(
	ctx context.Context,
	request windowsDesktopRequest,
) error {
	source := ""
	if request.Action == "import" && len(request.Config) != 0 {
		source = "config-bytes"
	}
	if err := validateWindowsDesktopRequest(
		request.Action, request.Name, source,
	); err != nil {
		return err
	}
	if request.Overwrite && request.Action != "import" {
		return fmt.Errorf(
			"desktop %s does not accept overwrite", request.Action,
		)
	}
	if request.Action == "import" {
		if err := validateWindowsDesktopConfigBytes(request.Config); err != nil {
			return err
		}
	} else if len(request.Config) != 0 {
		return fmt.Errorf(
			"desktop %s does not accept configuration contents",
			request.Action,
		)
	}

	switch request.Action {
	case "up", "down":
		return Manage(ctx, request.Action, request.Name)
	case "check":
		lease, _, err := openAndValidateWindowsStoredConfig(request.Name)
		if lease != nil {
			defer lease.Close()
		}
		return err
	case "import":
		host := platform.Current()
		return importWindowsDesktopConfigBytes(
			request.Config,
			host.ConfigPath(request.Name),
			request.Overwrite,
		)
	default:
		return fmt.Errorf(
			"unsupported desktop helper action %q", request.Action,
		)
	}
}

// ImportDesktopConfig validates the destination name before entering the
// Windows-specific atomic copy and ACL implementation.
func ImportDesktopConfig(
	source string,
	name string,
	overwrite bool,
) error {
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return err
	}
	programData, err := windowsProgramDataPath()
	if err != nil {
		return err
	}
	destination := filepath.Join(
		programData,
		windowsWGQUICRootDirectory,
		"interfaces",
		name+".conf",
	)
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve desktop import source: %w", err)
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve desktop import destination: %w", err)
	}
	if strings.EqualFold(sourcePath, destinationPath) {
		return errors.New("desktop import source and destination are identical")
	}
	contents, err := readWindowsDesktopConfig(sourcePath)
	if err != nil {
		return err
	}
	return importWindowsDesktopConfigBytes(
		contents, destinationPath, overwrite,
	)
}

func openWindowsDesktopPipe(path string) (net.Conn, error) {
	const prefix = `\\.\pipe\wg-quic-desktop-`
	if !strings.HasPrefix(path, prefix) ||
		len(path) <= len(prefix) ||
		len(path) > 256 ||
		strings.ContainsAny(strings.TrimPrefix(path, prefix), `\/`) {
		return nil, errors.New("invalid desktop pipe")
	}
	connection, err := namedpipe.DialTimeout(path, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to desktop pipe: %w", err)
	}
	return connection, nil
}

func readWindowsDesktopConfig(sourcePath string) ([]byte, error) {
	input, err := openWindowsReadOnlyNoFollowFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open desktop import source: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect desktop import source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("desktop import source must be a regular file")
	}
	if info.Size() > maxWindowsDesktopConfigSize {
		return nil, fmt.Errorf(
			"desktop import source is %d bytes; maximum is %d",
			info.Size(),
			maxWindowsDesktopConfigSize,
		)
	}
	contents, err := io.ReadAll(io.LimitReader(
		input, maxWindowsDesktopConfigSize+1,
	))
	if err != nil {
		return nil, fmt.Errorf("read desktop import source: %w", err)
	}
	if len(contents) > maxWindowsDesktopConfigSize {
		return nil, fmt.Errorf(
			"desktop import source exceeded %d bytes while reading",
			maxWindowsDesktopConfigSize,
		)
	}
	return contents, nil
}

func validateWindowsDesktopConfigBytes(contents []byte) error {
	if len(contents) == 0 {
		return errors.New("desktop import configuration is empty")
	}
	if len(contents) > maxWindowsDesktopConfigSize {
		return fmt.Errorf(
			"desktop import configuration is %d bytes; maximum is %d",
			len(contents), maxWindowsDesktopConfigSize,
		)
	}
	cfg, err := config.Parse(bytes.NewReader(contents))
	if err != nil {
		return err
	}
	return validateConfig(cfg)
}

// windowsStoredConfigLease pins the secure ProgramData directory chain and the
// configuration itself without share-write or share-delete access. Callers can
// retain it through service startup so the validated file cannot be swapped
// between validation and the service reaching Running.
type windowsStoredConfigLease struct {
	directories *windowsSecurePathLease
	file        *os.File
	path        string
}

func (l *windowsStoredConfigLease) Close() error {
	if l == nil {
		return nil
	}
	var errs []error
	if l.file != nil {
		errs = append(errs, l.file.Close())
		l.file = nil
	}
	if l.directories != nil {
		errs = append(errs, l.directories.Close())
		l.directories = nil
	}
	return errors.Join(errs...)
}

func openAndValidateWindowsStoredConfig(
	name string,
) (*windowsStoredConfigLease, *config.Config, error) {
	if err := platform.Current().ValidateInterfaceName(name); err != nil {
		return nil, nil, err
	}
	directory, directories, err := openWindowsSecureInterfacesDirectory()
	if err != nil {
		return nil, nil, err
	}
	lease := &windowsStoredConfigLease{
		directories: directories,
		path:        filepath.Join(directory, name+".conf"),
	}
	file, err := openWindowsSecureExistingFile(
		lease.path,
		"stored tunnel configuration",
	)
	if err != nil {
		_ = lease.Close()
		return nil, nil, fmt.Errorf("open stored tunnel configuration: %w", err)
	}
	lease.file = file
	cfg, err := config.Parse(file)
	if err != nil {
		_ = lease.Close()
		return nil, nil, err
	}
	if err := validateConfig(cfg); err != nil {
		_ = lease.Close()
		return nil, nil, err
	}
	return lease, cfg, nil
}

func importWindowsDesktopConfigBytes(
	contents []byte,
	destination string,
	overwrite bool,
) error {
	if err := validateWindowsDesktopConfigBytes(contents); err != nil {
		return err
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve desktop import destination: %w", err)
	}

	directory, directoryLease, err := openWindowsSecureInterfacesDirectory()
	if err != nil {
		return err
	}
	defer directoryLease.Close()
	if !strings.EqualFold(filepath.Clean(filepath.Dir(destinationPath)), directory) {
		return fmt.Errorf(
			"desktop import destination %q is outside the secured interfaces directory",
			destinationPath,
		)
	}

	output, err := createWindowsSecureTemporaryFile(
		directory,
		"."+filepath.Base(destinationPath)+".tmp-",
		"temporary desktop configuration",
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
	copied, err := io.Copy(output, bytes.NewReader(contents))
	if err != nil {
		output.Close()
		return fmt.Errorf("copy desktop configuration: %w", err)
	}
	if copied != int64(len(contents)) {
		output.Close()
		return fmt.Errorf(
			"desktop import copied %d bytes; expected %d",
			copied, len(contents),
		)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return fmt.Errorf("flush desktop configuration: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close desktop configuration: %w", err)
	}
	existing, err := openWindowsSecureExistingFile(
		destinationPath,
		"stored desktop configuration",
	)
	if err == nil {
		if closeErr := existing.Close(); closeErr != nil {
			return fmt.Errorf("close existing desktop configuration: %w", closeErr)
		}
	} else if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) &&
		!errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return fmt.Errorf("inspect existing desktop configuration: %w", err)
	}
	if err := moveWindowsDesktopConfig(
		temporaryPath,
		destinationPath,
		overwrite,
	); err != nil {
		return err
	}
	keepTemporary = false
	installed, err := openWindowsSecureExistingFile(
		destinationPath,
		"installed desktop configuration",
	)
	if err != nil {
		return fmt.Errorf("verify installed desktop configuration: %w", err)
	}
	if err := installed.Close(); err != nil {
		return fmt.Errorf("close installed desktop configuration: %w", err)
	}
	return nil
}

func moveWindowsDesktopConfig(
	source string,
	destination string,
	overwrite bool,
) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if overwrite {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	if err := windows.MoveFileEx(from, to, flags); err != nil {
		if !overwrite &&
			(errors.Is(err, windows.ERROR_ALREADY_EXISTS) ||
				errors.Is(err, windows.ERROR_FILE_EXISTS)) {
			return os.ErrExist
		}
		return fmt.Errorf("install desktop configuration: %w", err)
	}
	return nil
}
