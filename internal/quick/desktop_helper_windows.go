//go:build windows

package quick

import (
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

	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/ipc/namedpipe"
	"golang.org/x/sys/windows"
)

const maxWindowsDesktopConfigSize = 1024 * 1024

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
		N: maxWindowsDesktopConfigSize + 1,
	}
	var request windowsDesktopRequest
	if err := json.NewDecoder(limited).Decode(&request); err != nil {
		return windowsDesktopRequest{}, fmt.Errorf(
			"decode desktop helper request: %w", err,
		)
	}
	if limited.N == 0 {
		return windowsDesktopRequest{}, errors.New(
			"desktop helper request exceeded 1 MiB",
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
	if err := validateWindowsDesktopRequest(
		request.Action, request.Name, request.Source,
	); err != nil {
		return err
	}
	if request.Overwrite && request.Action != "import" {
		return fmt.Errorf(
			"desktop %s does not accept overwrite", request.Action,
		)
	}

	switch request.Action {
	case "up", "down":
		return Manage(ctx, request.Action, request.Name)
	case "check":
		return Check(request.Name)
	case "import":
		if err := Check(request.Source); err != nil {
			return err
		}
		return ImportDesktopConfig(
			request.Source,
			request.Name,
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
	return importWindowsDesktopConfig(
		source,
		host.ConfigPath(name),
		overwrite,
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

func importWindowsDesktopConfig(
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
	if strings.EqualFold(sourcePath, destinationPath) {
		return errors.New("desktop import source and destination are identical")
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect desktop import source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("desktop import source must be a regular file")
	}
	if info.Size() > maxWindowsDesktopConfigSize {
		return fmt.Errorf(
			"desktop import source is %d bytes; maximum is %d",
			info.Size(),
			maxWindowsDesktopConfigSize,
		)
	}

	directory := filepath.Dir(destinationPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create desktop configuration directory: %w", err)
	}
	if err := protectWindowsDesktopConfigDirectory(directory); err != nil {
		return err
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
		maxWindowsDesktopConfigSize+1,
	))
	if err != nil {
		output.Close()
		return fmt.Errorf("copy desktop configuration: %w", err)
	}
	if copied > maxWindowsDesktopConfigSize {
		output.Close()
		return fmt.Errorf(
			"desktop import source exceeded %d bytes while copying",
			maxWindowsDesktopConfigSize,
		)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return fmt.Errorf("flush desktop configuration: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close desktop configuration: %w", err)
	}
	if err := protectWindowsDesktopConfigFile(temporaryPath); err != nil {
		return err
	}
	if err := moveWindowsDesktopConfig(
		temporaryPath,
		destinationPath,
		overwrite,
	); err != nil {
		return err
	}
	keepTemporary = false
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

func protectWindowsDesktopConfigDirectory(path string) error {
	return protectWindowsDesktopPath(
		path,
		"D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;GRGX;;;BU)",
		"configuration directory",
	)
}

func protectWindowsDesktopConfigFile(path string) error {
	return protectWindowsDesktopPath(
		path,
		"D:P(A;;FA;;;SY)(A;;FA;;;BA)",
		"configuration file",
	)
}

func protectWindowsDesktopPath(
	path string,
	sddl string,
	description string,
) error {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("create Windows desktop %s ACL: %w", description, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read Windows desktop %s ACL: %w", description, err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf(
			"protect Windows desktop %s %q: %w",
			description,
			path,
			err,
		)
	}
	return nil
}
