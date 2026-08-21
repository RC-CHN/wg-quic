package management

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

const (
	maxRequestSize          = 1 << 20
	maxRequiredCapabilities = 32
	readTimeout             = 5 * time.Second
	writeTimeout            = 5 * time.Second
	defaultCallTimeout      = 2*time.Minute + 10*time.Second
)

type Server struct {
	ctx      context.Context
	listener net.Listener
	handler  Handler
	cleanup  func() error
	done     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func Start(ctx context.Context, path string, handler Handler) (*Server, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if path == "" {
		return nil, errors.New("management endpoint path is required")
	}
	if handler == nil {
		return nil, errors.New("management handler is required")
	}
	listener, cleanup, err := listen(path)
	if err != nil {
		return nil, err
	}
	server := &Server{
		ctx: ctx, listener: listener, handler: handler, cleanup: cleanup,
		done: make(chan struct{}),
	}
	server.wg.Add(1)
	go server.accept()
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-server.done:
		}
	}()
	return server, nil
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer connection.Close()
			s.serve(connection)
		}()
	}
}

func (s *Server) serve(connection net.Conn) {
	_ = connection.SetReadDeadline(time.Now().Add(readTimeout))
	request, failure := decodeRequest(connection)
	if failure != nil {
		s.writeResponse(connection, Response{ProtocolVersion: ProtocolVersion, Failure: failure})
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	response := validateAndDispatch(s.ctx, s.handler, request)
	s.writeResponse(connection, response)
}

func (s *Server) writeResponse(connection net.Conn, response Response) {
	response.ProtocolVersion = ProtocolVersion
	_ = connection.SetWriteDeadline(time.Now().Add(writeTimeout))
	_ = json.NewEncoder(connection).Encode(response)
}

func decodeRequest(reader io.Reader) (Request, *reconcile.Failure) {
	buffered := bufio.NewReaderSize(reader, maxRequestSize+1)
	line, err := buffered.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxRequestSize {
		return Request{}, protocolFailure("request_too_large", "management request exceeds 1 MiB")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return Request{}, protocolFailure("decode_failed", fmt.Sprintf("read management request: %v", err))
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return Request{}, protocolFailure("decode_failed", "management request is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, protocolFailure("decode_failed", fmt.Sprintf("decode management request: %v", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Request{}, protocolFailure("decode_failed", "management request contains trailing JSON")
		}
		return Request{}, protocolFailure("decode_failed", fmt.Sprintf("decode trailing management data: %v", err))
	}
	return request, nil
}

func validateAndDispatch(ctx context.Context, handler Handler, request Request) Response {
	if request.ProtocolVersion != ProtocolVersion {
		return ErrorResponse(
			"unsupported_protocol",
			fmt.Sprintf("management protocol version %d is unsupported", request.ProtocolVersion),
			false,
		)
	}
	if request.Interface == "" {
		return ErrorResponse("validation_failed", "interface is required", false)
	}
	seenCapabilities := make(map[string]struct{}, len(request.RequiredCapabilities))
	if len(request.RequiredCapabilities) > maxRequiredCapabilities {
		return ErrorResponse("validation_failed", "too many required capabilities", false)
	}
	for _, capability := range request.RequiredCapabilities {
		if capability == "" || len(capability) > 128 {
			return ErrorResponse("validation_failed", "required capability is invalid", false)
		}
		if _, exists := seenCapabilities[capability]; exists {
			return ErrorResponse("validation_failed", "required capability is duplicated", false)
		}
		seenCapabilities[capability] = struct{}{}
	}
	switch request.Operation {
	case OperationStatus, OperationReconcile, OperationReload,
		OperationTransactionStatus, OperationRefreshEndpoints:
	default:
		return ErrorResponse(
			"unsupported_operation",
			fmt.Sprintf("management operation %q is unsupported", request.Operation),
			false,
		)
	}
	response := handler.Handle(ctx, request)
	response.ProtocolVersion = ProtocolVersion
	return response
}

func protocolFailure(code, message string) *reconcile.Failure {
	return &reconcile.Failure{
		Code: code, Stage: reconcile.StateValidating, Message: message,
	}
}

func (s *Server) Close() error {
	var result error
	s.once.Do(func() {
		result = s.listener.Close()
		s.wg.Wait()
		if s.cleanup != nil {
			result = errors.Join(result, s.cleanup())
		}
		close(s.done)
	})
	return result
}

type Client struct {
	path string
}

func NewClient(path string) *Client {
	return &Client{path: path}
}

func (c *Client) Call(ctx context.Context, request Request) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.ProtocolVersion == 0 {
		request.ProtocolVersion = ProtocolVersion
	}
	callCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	connection, err := dial(callCtx, c.path)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	if deadline, exists := callCtx.Deadline(); exists {
		_ = connection.SetDeadline(deadline)
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return Response{}, err
	}
	response, err := decodeResponse(connection)
	if err != nil {
		return Response{}, err
	}
	if response.ProtocolVersion != ProtocolVersion {
		return Response{}, fmt.Errorf("management response protocol version %d is unsupported", response.ProtocolVersion)
	}
	return response, nil
}

// Responses are additive within one protocol version. Older clients ignore
// new optional status fields, while the server remains strict about requests
// because accepting an unknown mutation field would be unsafe.
func decodeResponse(reader io.Reader) (Response, error) {
	var response Response
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		return Response{}, err
	}
	return response, nil
}

func (c *Client) Status(ctx context.Context, interfaceName string) (Status, error) {
	response, err := c.Call(ctx, Request{
		ProtocolVersion: ProtocolVersion,
		Operation:       OperationStatus,
		Interface:       interfaceName,
	})
	if err != nil {
		return Status{}, err
	}
	if response.Failure != nil {
		return Status{}, errors.New(response.Failure.Message)
	}
	if response.Status == nil {
		return Status{}, errors.New("management response did not include status")
	}
	return *response.Status, nil
}
