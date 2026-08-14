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
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"golang.org/x/sys/windows/svc"
)

const (
	windowsManagementServiceName        = "wg-quic-manager"
	windowsManagementProtocolVersion    = 1
	windowsManagementMaxEnvelopeSize    = 2*maxWindowsDesktopConfigSize + 64*1024
	windowsManagementMaxConnections     = 8
	windowsManagementHandshakeTimeout   = 2 * time.Second
	windowsManagementRequestReadTimeout = 10 * time.Second
)

var windowsManagementPreamble = []byte("WGQM\n")

const (
	windowsManagementCodeOK             = "ok"
	windowsManagementCodeContinue       = "continue"
	windowsManagementCodeUnauthorized   = "unauthorized"
	windowsManagementCodeInvalidRequest = "invalid_request"
	windowsManagementCodeIncompatible   = "incompatible_protocol"
	windowsManagementCodeElevation      = "elevation_required"
	windowsManagementCodeOperation      = "operation_failed"
)

var (
	errWindowsManagementUnavailable = errors.New(
		"wg-quic management service is unavailable",
	)
	errWindowsManagementIncompatible = errors.New(
		"wg-quic management service protocol is incompatible",
	)
	errWindowsManagementElevationRequired = errors.New(
		"wg-quic management operation requires administrator approval",
	)
	errWindowsManagementOutcomeUnknown = errors.New(
		"wg-quic management operation outcome is unknown",
	)
	windowsManagementDial             = dialWindowsManagementPipe
	windowsManagementOpenStoredConfig = openAndValidateWindowsStoredConfig
	windowsManagementReadStoredConfig = readWindowsStoredDesktopConfig
)

const (
	windowsManagementStatusReady        = "ready"
	windowsManagementStatusUnauthorized = "unauthorized"
	windowsManagementStatusUnavailable  = "unavailable"
	windowsManagementStatusIncompatible = "incompatible"
	windowsManagementStatusError        = "error"
)

// WindowsDesktopBrokerStatus is the stable, read-only result returned to the
// desktop when it probes the MSI-owned management service. Expected service
// states are data, not command failures, so callers can render them without
// accidentally entering the UAC fallback path.
type WindowsDesktopBrokerStatus struct {
	Status          string `json:"status"`
	ServiceName     string `json:"service_name"`
	ProtocolVersion int    `json:"protocol_version"`
	Message         string `json:"message,omitempty"`
}

// QueryWindowsDesktopBrokerStatus performs a bounded broker-only probe. It
// never launches the elevated desktop helper or otherwise requests UAC.
func QueryWindowsDesktopBrokerStatus(
	ctx context.Context,
) WindowsDesktopBrokerStatus {
	probeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	message, err := runWindowsManagementClient(
		probeContext, "probe", "", "", false,
	)
	return windowsDesktopBrokerStatus(message, err)
}

func windowsDesktopBrokerStatus(
	message string,
	err error,
) WindowsDesktopBrokerStatus {
	result := WindowsDesktopBrokerStatus{
		Status:          windowsManagementStatusReady,
		ServiceName:     windowsManagementServiceName,
		ProtocolVersion: windowsManagementProtocolVersion,
		Message:         message,
	}
	if err == nil {
		return result
	}
	result.Message = err.Error()
	switch {
	case errors.Is(err, errWindowsManagementUnauthorized):
		result.Status = windowsManagementStatusUnauthorized
	case errors.Is(err, errWindowsManagementIncompatible):
		result.Status = windowsManagementStatusIncompatible
	case errors.Is(err, errWindowsManagementUnavailable):
		result.Status = windowsManagementStatusUnavailable
	default:
		result.Status = windowsManagementStatusError
	}
	return result
}

func runWindowsManagementClient(
	ctx context.Context,
	action string,
	name string,
	source string,
	overwrite bool,
) (string, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return "", errors.New("desktop operation context has no deadline")
	}
	request := windowsManagementRequest{
		ProtocolVersion:    windowsManagementProtocolVersion,
		Action:             action,
		Name:               name,
		Overwrite:          overwrite,
		DeadlineUnixMillis: deadline.UnixMilli(),
	}
	if action == "import" {
		contents, err := readWindowsDesktopConfig(source)
		if err != nil {
			return "", err
		}
		if err := validateWindowsDesktopConfigBytes(contents); err != nil {
			return "", err
		}
		request.Config = contents
	}
	handshakeContext, cancelHandshake := context.WithTimeout(
		ctx, windowsManagementHandshakeTimeout,
	)
	connection, err := windowsManagementDial(handshakeContext)
	cancelHandshake()
	if err != nil {
		return "", fmt.Errorf(
			"%w: %w", errWindowsManagementUnavailable, err,
		)
	}
	defer connection.Close()
	handshakeDeadline := time.Now().Add(windowsManagementHandshakeTimeout)
	if deadline.Before(handshakeDeadline) {
		handshakeDeadline = deadline
	}
	_ = connection.SetDeadline(handshakeDeadline)
	if _, err := connection.Write(windowsManagementPreamble); err != nil {
		return "", fmt.Errorf(
			"%w: send protocol preamble: %w",
			errWindowsManagementUnavailable, err,
		)
	}
	handshake, err := readWindowsManagementResult(connection)
	if err != nil {
		return "", fmt.Errorf(
			"%w: read authorization result: %w",
			errWindowsManagementUnavailable, err,
		)
	}
	if handshake.ProtocolVersion != windowsManagementProtocolVersion {
		return "", fmt.Errorf(
			"%w: service returned version %d",
			errWindowsManagementIncompatible,
			handshake.ProtocolVersion,
		)
	}
	if !handshake.Success {
		return "", windowsManagementResultError(handshake)
	}
	if handshake.Code != windowsManagementCodeContinue {
		return "", fmt.Errorf(
			"%w: management service returned an invalid authorization handshake",
			errWindowsManagementUnavailable,
		)
	}
	_ = connection.SetDeadline(deadline)
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return "", fmt.Errorf(
			"%w: send request: %w",
			errWindowsManagementOutcomeUnknown, err,
		)
	}
	result, err := readWindowsManagementResult(connection)
	if err != nil {
		return "", fmt.Errorf(
			"%w: read service result: %w",
			errWindowsManagementOutcomeUnknown, err,
		)
	}
	if result.ProtocolVersion != windowsManagementProtocolVersion {
		return "", fmt.Errorf(
			"%w: service returned version %d after request dispatch",
			errWindowsManagementOutcomeUnknown,
			result.ProtocolVersion,
		)
	}
	message := result.Message
	if result.Success {
		if result.Code != "" && result.Code != windowsManagementCodeOK {
			return "", errors.New(
				"management service returned an inconsistent success result",
			)
		}
		if action == "read" {
			return result.Contents, nil
		}
		return message, nil
	}
	return "", windowsManagementResultError(result)
}

func windowsManagementResultError(result windowsManagementResult) error {
	message := result.Message
	if message == "" {
		message = "the wg-quic management service rejected the request"
	}
	switch result.Code {
	case windowsManagementCodeUnauthorized:
		return fmt.Errorf(
			"%w: %s", errWindowsManagementUnauthorized, message,
		)
	case windowsManagementCodeIncompatible:
		return fmt.Errorf(
			"%w: %s", errWindowsManagementIncompatible, message,
		)
	case windowsManagementCodeElevation:
		return fmt.Errorf(
			"%w: %s", errWindowsManagementElevationRequired, message,
		)
	default:
		return errors.New(message)
	}
}

func shouldUseWindowsDesktopElevationFallback(err error) bool {
	return errors.Is(err, errWindowsManagementUnavailable) ||
		errors.Is(err, errWindowsManagementUnauthorized) ||
		errors.Is(err, errWindowsManagementIncompatible) ||
		errors.Is(err, errWindowsManagementElevationRequired)
}

type windowsManagementRequest struct {
	ProtocolVersion    int    `json:"protocol_version"`
	Action             string `json:"action"`
	Name               string `json:"name"`
	Overwrite          bool   `json:"overwrite,omitempty"`
	DeadlineUnixMillis int64  `json:"deadline_unix_millis"`
	Config             []byte `json:"config,omitempty"`
}

type windowsManagementResult struct {
	ProtocolVersion int    `json:"protocol_version"`
	Success         bool   `json:"success"`
	Code            string `json:"code"`
	Message         string `json:"message,omitempty"`
	Contents        string `json:"contents,omitempty"`
}

// RunWindowsManagementService runs the MSI-owned, long-lived privilege
// boundary. The WebView and Tauri process remain unelevated; this service
// accepts only the small, authenticated management protocol below.
func RunWindowsManagementService() error {
	return svc.Run(
		windowsManagementServiceName,
		&windowsManagementService{serve: serveWindowsManagement},
	)
}

type windowsManagementService struct {
	serve             func(context.Context, func()) error
	shutdownTimeout   time.Duration
	heartbeatInterval time.Duration
	logf              func(string, ...any)
}

func (s *windowsManagementService) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	changes chan<- svc.Status,
) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	heartbeat := s.heartbeatInterval
	if heartbeat <= 0 {
		heartbeat = defaultWindowsServiceHeartbeat
	}
	checkpoint := uint32(1)
	startupStatus := func() svc.Status {
		return svc.Status{
			State:      svc.StartPending,
			CheckPoint: checkpoint,
			WaitHint:   uint32(windowsServiceWaitHint / time.Millisecond),
		}
	}
	changes <- startupStatus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	var readyOnce sync.Once
	result := make(chan error, 1)
	go func() {
		result <- s.serve(ctx, func() {
			readyOnce.Do(func() { close(ready) })
		})
	}()
	startupTicker := time.NewTicker(heartbeat)
	startupComplete := false
	for !startupComplete {
		select {
		case err := <-result:
			startupTicker.Stop()
			return false, windowsManagementServiceExitCode(err)
		case <-ready:
			startupTicker.Stop()
			changes <- svc.Status{State: svc.Running, Accepts: accepted}
			startupComplete = true
		case request, ok := <-requests:
			if !ok {
				startupTicker.Stop()
				return s.stop(cancel, nil, result, changes)
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- startupStatus()
			case svc.Stop, svc.Shutdown:
				startupTicker.Stop()
				return s.stop(cancel, requests, result, changes)
			}
		case <-startupTicker.C:
			checkpoint++
			changes <- startupStatus()
		}
	}
	for {
		select {
		case err := <-result:
			return false, windowsManagementServiceExitCode(err)
		case request, ok := <-requests:
			if !ok {
				return s.stop(cancel, nil, result, changes)
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				return s.stop(cancel, requests, result, changes)
			}
		}
	}
}

func (s *windowsManagementService) stop(
	cancel context.CancelFunc,
	requests <-chan svc.ChangeRequest,
	result <-chan error,
	changes chan<- svc.Status,
) (bool, uint32) {
	timeout := s.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultWindowsServiceShutdownTimeout
	}
	heartbeat := s.heartbeatInterval
	if heartbeat <= 0 {
		heartbeat = defaultWindowsServiceHeartbeat
	}
	checkpoint := uint32(1)
	status := func() svc.Status {
		return svc.Status{
			State: svc.StopPending, CheckPoint: checkpoint,
			WaitHint: uint32(windowsServiceWaitHint / time.Millisecond),
		}
	}
	changes <- status()
	cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return false, windowsManagementServiceExitCode(err)
		case request, ok := <-requests:
			if !ok {
				requests = nil
			} else if request.Cmd == svc.Interrogate {
				changes <- status()
			}
		case <-ticker.C:
			checkpoint++
			changes <- status()
		case <-timer.C:
			if s.logf != nil {
				s.logf(
					"wg-quic management service shutdown timed out after %s",
					timeout,
				)
			}
			return false, 1
		}
	}
}

func windowsManagementServiceExitCode(err error) uint32 {
	if err != nil {
		return 1
	}
	return 0
}

func serveWindowsManagement(
	ctx context.Context,
	ready func(),
) error {
	sweepWindowsOrphanRuntimesBestEffort(ctx)
	listener, err := newWindowsManagementPipeListener()
	if err != nil {
		return fmt.Errorf("listen for desktop management requests: %w", err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-closed:
		}
	}()
	defer close(closed)
	ready()

	var handlers sync.WaitGroup
	connections := make(chan struct{}, windowsManagementMaxConnections)
	var mutations sync.Mutex
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				handlers.Wait()
				return nil
			}
			return fmt.Errorf("accept desktop management request: %w", err)
		}
		select {
		case connections <- struct{}{}:
		case <-ctx.Done():
			_ = connection.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-connections }()
			defer connection.Close()
			finished := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					_ = connection.Close()
				case <-finished:
				}
			}()
			_ = handleWindowsManagementConnection(
				ctx, connection, &mutations,
			)
			close(finished)
		}()
	}
}

func handleWindowsManagementConnection(
	ctx context.Context,
	connection net.Conn,
	mutations *sync.Mutex,
) error {
	_ = connection.SetDeadline(
		time.Now().Add(windowsManagementHandshakeTimeout),
	)
	preambleErr := readWindowsManagementPreamble(connection)
	if preambleErr != nil {
		return writeWindowsManagementResult(connection, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Code:            windowsManagementCodeInvalidRequest,
			Message:         preambleErr.Error(),
		})
	}
	if err := authorizeWindowsManagementPipeClient(connection); err != nil {
		code := windowsManagementCodeOperation
		message := "authorize desktop management request: " + err.Error()
		if errors.Is(err, errWindowsManagementUnauthorized) {
			code = windowsManagementCodeUnauthorized
			message = "the current Windows account is not authorized for passwordless wg-quic management"
		}
		return writeWindowsManagementResult(connection, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Code:            code,
			Message:         message,
		})
	}
	if err := writeWindowsManagementResult(connection, windowsManagementResult{
		ProtocolVersion: windowsManagementProtocolVersion,
		Success:         true,
		Code:            windowsManagementCodeContinue,
	}); err != nil {
		return err
	}
	_ = connection.SetDeadline(
		time.Now().Add(windowsManagementRequestReadTimeout),
	)
	request, err := readWindowsManagementRequest(connection)
	if err != nil {
		return writeWindowsManagementResult(connection, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Code:            windowsManagementCodeInvalidRequest,
			Message:         err.Error(),
		})
	}
	if request.ProtocolVersion != windowsManagementProtocolVersion {
		return writeWindowsManagementResult(connection, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Code:            windowsManagementCodeIncompatible,
			Message: fmt.Sprintf(
				"management protocol version %d is unsupported",
				request.ProtocolVersion,
			),
		})
	}
	deadline, err := windowsManagementRequestDeadline(request, time.Now())
	if err == nil {
		err = validateWindowsManagementRequest(request)
	}
	if err != nil {
		return writeWindowsManagementResult(connection, windowsManagementResult{
			ProtocolVersion: windowsManagementProtocolVersion,
			Code: windowsManagementFailureCode(
				err, windowsManagementCodeInvalidRequest,
			),
			Message: err.Error(),
		})
	}
	_ = connection.SetDeadline(deadline.Add(windowsDesktopResultGrace))
	operationContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	contents, err := runWindowsManagementOperation(
		operationContext, request, mutations,
	)
	result := windowsManagementResult{
		ProtocolVersion: windowsManagementProtocolVersion,
		Success:         err == nil,
		Code:            windowsManagementCodeOK,
		Contents:        contents,
	}
	if err != nil {
		result.Code = windowsManagementFailureCode(
			err, windowsManagementCodeOperation,
		)
		result.Message = windowsDesktopOperationEndError(err).Error()
	} else if request.Action == "check" {
		result.Message = "configuration is valid for wg-quic-quick"
	} else if request.Action == "probe" {
		result.Message = "wg-quic management service is ready"
	}
	return writeWindowsManagementResult(connection, result)
}

func windowsManagementFailureCode(err error, fallback string) string {
	if errors.Is(err, errWindowsManagementElevationRequired) {
		return windowsManagementCodeElevation
	}
	return fallback
}

func readWindowsManagementPreamble(reader io.Reader) error {
	got := make([]byte, len(windowsManagementPreamble))
	if _, err := io.ReadFull(reader, got); err != nil {
		return fmt.Errorf("read management protocol preamble: %w", err)
	}
	if !bytes.Equal(got, windowsManagementPreamble) {
		return errors.New("invalid management protocol preamble")
	}
	return nil
}

func readWindowsManagementRequest(
	reader io.Reader,
) (windowsManagementRequest, error) {
	limited := &io.LimitedReader{
		R: reader, N: windowsManagementMaxEnvelopeSize + 1,
	}
	var request windowsManagementRequest
	if err := json.NewDecoder(limited).Decode(&request); err != nil {
		return windowsManagementRequest{}, fmt.Errorf(
			"decode management request: %w", err,
		)
	}
	if limited.N == 0 {
		return windowsManagementRequest{}, errors.New(
			"management request exceeded its size limit",
		)
	}
	return request, nil
}

func readWindowsManagementResult(
	reader io.Reader,
) (windowsManagementResult, error) {
	limited := &io.LimitedReader{
		R: reader, N: windowsManagementMaxEnvelopeSize + 1,
	}
	var result windowsManagementResult
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return windowsManagementResult{}, fmt.Errorf(
			"decode management result: %w", err,
		)
	}
	if limited.N == 0 {
		return windowsManagementResult{}, errors.New(
			"management result exceeded its size limit",
		)
	}
	return result, nil
}

func writeWindowsManagementResult(
	w io.Writer,
	result windowsManagementResult,
) error {
	if err := json.NewEncoder(w).Encode(result); err != nil {
		return fmt.Errorf("report desktop management result: %w", err)
	}
	return nil
}

func windowsManagementRequestDeadline(
	request windowsManagementRequest,
	now time.Time,
) (time.Time, error) {
	if request.DeadlineUnixMillis <= 0 {
		return time.Time{}, errors.New(
			"management request deadline is required",
		)
	}
	deadline := time.UnixMilli(request.DeadlineUnixMillis)
	maximum := now.Add(windowsDesktopOperationTimeout)
	if deadline.After(maximum) {
		deadline = maximum
	}
	if !deadline.After(now) {
		return time.Time{}, errors.New(
			"management request deadline expired before it was received",
		)
	}
	return deadline, nil
}

func validateWindowsManagementRequest(
	request windowsManagementRequest,
) error {
	if request.Action == "probe" {
		if request.Name != "" || request.Overwrite || len(request.Config) != 0 {
			return errors.New(
				"desktop probe does not accept interface or configuration data",
			)
		}
		return nil
	}
	source := ""
	if request.Action == "import" {
		source = "config-bytes"
	}
	if err := validateWindowsDesktopRequest(
		request.Action,
		request.Name,
		source,
	); err != nil {
		return err
	}
	if request.Overwrite && request.Action != "import" {
		return fmt.Errorf(
			"desktop %s does not accept overwrite", request.Action,
		)
	}
	if request.Action == "import" {
		return validateWindowsManagementConfigBytes(request.Config)
	}
	if len(request.Config) != 0 {
		return fmt.Errorf(
			"desktop %s does not accept configuration contents",
			request.Action,
		)
	}
	return nil
}

func runWindowsManagementOperation(
	ctx context.Context,
	request windowsManagementRequest,
	mutations *sync.Mutex,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if request.Action == "up" ||
		request.Action == "down" ||
		request.Action == "import" ||
		request.Action == "delete" {
		mutations.Lock()
		defer mutations.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch request.Action {
	case "up":
		if err := validateWindowsManagementStoredConfig(
			request.Name,
		); err != nil {
			return "", err
		}
		return "", manageWindowsBroker(ctx, request.Action, request.Name)
	case "down":
		return "", manageWindowsBroker(ctx, request.Action, request.Name)
	case "check":
		lease, _, err := windowsManagementOpenStoredConfig(request.Name)
		if err != nil {
			return "", err
		}
		return "", lease.Close()
	case "read":
		return windowsManagementReadStoredConfig(request.Name)
	case "import":
		if err := validateWindowsManagementConfigBytes(
			request.Config,
		); err != nil {
			return "", err
		}
		host := platform.Current()
		return "", importWindowsDesktopConfigBytes(
			request.Config,
			host.ConfigPath(request.Name),
			request.Overwrite,
		)
	case "delete":
		// Best-effort stop before removal so a running tunnel does not
		// outlive its configuration.
		_ = manageWindowsBroker(ctx, "down", request.Name)
		return "", DeleteDesktopConfig(request.Name)
	case "probe":
		return "", nil
	default:
		return "", fmt.Errorf(
			"unsupported desktop management action %q",
			request.Action,
		)
	}
}

func validateWindowsManagementConfigBytes(contents []byte) error {
	if err := validateWindowsDesktopConfigBytes(contents); err != nil {
		return err
	}
	cfg, err := config.Parse(bytes.NewReader(contents))
	if err != nil {
		return err
	}
	return validateWindowsManagementHookFreeConfig(cfg)
}

func validateWindowsManagementStoredConfig(name string) error {
	lease, cfg, err := windowsManagementOpenStoredConfig(name)
	if err != nil {
		return err
	}
	defer lease.Close()
	return validateWindowsManagementHookFreeConfig(cfg)
}

func validateWindowsManagementHookFreeConfig(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("management configuration is nil")
	}
	if len(cfg.Interface.PreUp) == 0 &&
		len(cfg.Interface.PostUp) == 0 &&
		len(cfg.Interface.PreDown) == 0 &&
		len(cfg.Interface.PostDown) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: configurations with PreUp, PostUp, PreDown, or PostDown hooks cannot use the persistent management service",
		errWindowsManagementElevationRequired,
	)
}
