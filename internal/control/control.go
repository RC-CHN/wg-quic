package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

const requestTimeout = 5 * time.Second

type Status struct {
	Interface  string          `json:"interface"`
	State      string          `json:"state"`
	ListenPort uint16          `json:"listen_port"`
	Carrier    string          `json:"carrier"`
	FECMode    string          `json:"fec_mode"`
	ObfsMode   string          `json:"obfs_mode"`
	Peers      []PeerStatus    `json:"peers,omitempty"`
	Stats      telemetry.Stats `json:"stats"`
}

type PeerStatus struct {
	PublicKey       string `json:"public_key"`
	Endpoint        string `json:"endpoint,omitempty"`
	Generation      uint64 `json:"generation"`
	Session         string `json:"session"`
	LatestHandshake int64  `json:"latest_handshake,omitempty"`
	TransferRx      uint64 `json:"transfer_rx,omitempty"`
	TransferTx      uint64 `json:"transfer_tx,omitempty"`
}

type SetPeerEndpointRequest struct {
	PublicKey  string `json:"public_key"`
	Endpoint   string `json:"endpoint"`
	Generation uint64 `json:"generation"`
}

type Handler struct {
	Status          func() Status
	SetPeerEndpoint func(SetPeerEndpointRequest) error
	RedialPeer      func(string) error
	Activate        func() error
}

// Client is the transport-independent core control surface used by quick.
// LocalClient currently carries it over a Unix socket or Windows named pipe.
type Client interface {
	Status() (Status, error)
	SetPeerEndpoint(SetPeerEndpointRequest) error
	RedialPeer(publicKey string) error
	Activate() error
}

type LocalClient struct {
	path string
}

func NewClient(path string) Client {
	return &LocalClient{path: path}
}

type request struct {
	Operation       string                  `json:"operation"`
	SetPeerEndpoint *SetPeerEndpointRequest `json:"set_peer_endpoint,omitempty"`
	PublicKey       string                  `json:"public_key,omitempty"`
}

type response struct {
	Status *Status `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
}

type Server struct {
	listener net.Listener
	done     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
	cleanup  func() error
}

func Start(ctx context.Context, path string, status func() Status) (*Server, error) {
	return StartHandler(ctx, path, Handler{Status: status})
}

func StartHandler(ctx context.Context, path string, handler Handler) (*Server, error) {
	if handler.Status == nil {
		return nil, errors.New("status provider is required")
	}
	listener, cleanup, err := listen(path)
	if err != nil {
		return nil, err
	}
	return startHandler(ctx, listener, cleanup, handler), nil
}

// StartReadOnlyStatus exposes a second, status-only endpoint on platforms
// where the primary control endpoint is restricted to privileged callers.
// The transport decides whether a separate endpoint is required.
func StartReadOnlyStatus(
	ctx context.Context,
	path string,
	status func() Status,
) (*Server, error) {
	if status == nil {
		return nil, errors.New("status provider is required")
	}
	listener, cleanup, err := listenReadOnlyStatus(path)
	if err != nil {
		return nil, err
	}
	if listener == nil {
		return nil, nil
	}
	return startHandler(
		ctx,
		listener,
		cleanup,
		Handler{Status: status},
	), nil
}

func startHandler(
	ctx context.Context,
	listener net.Listener,
	cleanup func() error,
	handler Handler,
) *Server {
	server := &Server{
		listener: listener,
		done:     make(chan struct{}),
		cleanup:  cleanup,
	}
	server.wg.Add(1)
	go server.accept(handler)
	go func() {
		select {
		case <-ctx.Done():
			server.Close()
		case <-server.done:
		}
	}()
	return server
}

func (s *Server) accept(handler Handler) {
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
			_ = connection.SetDeadline(time.Now().Add(requestTimeout))
			var req request
			if err := json.NewDecoder(connection).Decode(&req); err != nil {
				_ = json.NewEncoder(connection).Encode(response{Error: fmt.Sprintf("decode control request: %v", err)})
				return
			}
			resp := dispatch(handler, req)
			_ = json.NewEncoder(connection).Encode(resp)
		}()
	}
}

func dispatch(handler Handler, req request) response {
	switch req.Operation {
	case "status":
		status := handler.Status()
		return response{Status: &status}
	case "set_peer_endpoint":
		if req.SetPeerEndpoint == nil {
			return response{Error: "set_peer_endpoint payload is required"}
		}
		if handler.SetPeerEndpoint == nil {
			return response{Error: "set_peer_endpoint is not supported"}
		}
		if err := handler.SetPeerEndpoint(*req.SetPeerEndpoint); err != nil {
			return response{Error: err.Error()}
		}
		return response{}
	case "redial_peer":
		if req.PublicKey == "" {
			return response{Error: "public_key is required"}
		}
		if handler.RedialPeer == nil {
			return response{Error: "redial_peer is not supported"}
		}
		if err := handler.RedialPeer(req.PublicKey); err != nil {
			return response{Error: err.Error()}
		}
		return response{}
	case "activate":
		if handler.Activate == nil {
			return response{Error: "activate is not supported"}
		}
		if err := handler.Activate(); err != nil {
			return response{Error: err.Error()}
		}
		return response{}
	default:
		return response{Error: fmt.Sprintf("unsupported control operation %q", req.Operation)}
	}
}

func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		err = s.listener.Close()
		s.wg.Wait()
		if s.cleanup != nil {
			err = errors.Join(err, s.cleanup())
		}
		close(s.done)
	})
	return err
}

func Read(path string) (Status, error) {
	return NewClient(path).Status()
}

func (c *LocalClient) Status() (Status, error) {
	var resp response
	err := c.callAt(c.path, request{Operation: "status"}, &resp)
	if err != nil {
		fallback := readOnlyStatusPath(c.path)
		if fallback == "" {
			return Status{}, err
		}
		resp = response{}
		if fallbackErr := c.callAt(
			fallback,
			request{Operation: "status"},
			&resp,
		); fallbackErr != nil {
			return Status{}, errors.Join(err, fallbackErr)
		}
	}
	if resp.Status == nil {
		return Status{}, errors.New("control response did not include status")
	}
	return *resp.Status, nil
}

func SetPeerEndpoint(path string, update SetPeerEndpointRequest) error {
	return NewClient(path).SetPeerEndpoint(update)
}

func (c *LocalClient) SetPeerEndpoint(update SetPeerEndpointRequest) error {
	var resp response
	return c.call(request{Operation: "set_peer_endpoint", SetPeerEndpoint: &update}, &resp)
}

func RedialPeer(path, publicKey string) error {
	return NewClient(path).RedialPeer(publicKey)
}

func (c *LocalClient) RedialPeer(publicKey string) error {
	var resp response
	return c.call(request{Operation: "redial_peer", PublicKey: publicKey}, &resp)
}

func (c *LocalClient) Activate() error {
	var resp response
	return c.call(request{Operation: "activate"}, &resp)
}

func (c *LocalClient) call(req request, resp *response) error {
	return c.callAt(c.path, req, resp)
}

func (c *LocalClient) callAt(
	path string,
	req request,
	resp *response,
) error {
	connection, err := dial(path, 2*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(requestTimeout))
	if err := json.NewEncoder(connection).Encode(req); err != nil {
		return err
	}
	if err := json.NewDecoder(connection).Decode(resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}
