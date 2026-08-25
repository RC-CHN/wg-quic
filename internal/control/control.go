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
	Interface               string                               `json:"interface"`
	State                   string                               `json:"state"`
	ListenPort              uint16                               `json:"listen_port"`
	Carrier                 string                               `json:"carrier"`
	FECMode                 string                               `json:"fec_mode"`
	ObfsMode                string                               `json:"obfs_mode"`
	Addresses               []string                             `json:"addresses,omitempty"`
	Peers                   []PeerStatus                         `json:"peers,omitempty"`
	Sessions                []telemetry.SessionObservation       `json:"sessions,omitempty"`
	SessionTelemetryOmitted uint64                               `json:"session_telemetry_omitted,omitempty"`
	RecentSessions          []telemetry.ClosedSessionObservation `json:"recent_sessions,omitempty"`
	RecentSessionsEvicted   uint64                               `json:"recent_sessions_evicted_total,omitempty"`
	Capabilities            []string                             `json:"capabilities,omitempty"`
	Stats                   telemetry.Stats                      `json:"stats"`
}

type PeerStatus struct {
	PublicKey string `json:"public_key"`
	// Endpoint is WireGuard's live endpoint and preserves the pre-reconcile
	// status contract. It may differ from SelectedEndpoint after authenticated
	// QUIC path roaming or an outer NAT rebinding.
	Endpoint string `json:"endpoint,omitempty"`
	// SelectedEndpoint is the numeric endpoint owned by Generation. Core
	// transactions, reconnect accounting, and resource cleanup use this field;
	// they must not mistake a transient roaming address for desired state.
	SelectedEndpoint             string `json:"selected_endpoint,omitempty"`
	Generation                   uint64 `json:"generation"`
	AuthenticatedGeneration      uint64 `json:"authenticated_endpoint_generation,omitempty"`
	AuthenticatedEndpoint        string `json:"authenticated_endpoint,omitempty"`
	Session                      string `json:"session"`
	LatestHandshake              int64  `json:"latest_handshake,omitempty"`
	LastRx                       int64  `json:"last_rx,omitempty"`
	LastTx                       int64  `json:"last_tx,omitempty"`
	LastActivity                 int64  `json:"last_activity,omitempty"`
	LastDirection                string `json:"last_activity_direction,omitempty"`
	ReconnectAttempts            uint64 `json:"reconnect_attempts,omitempty"`
	ReconnectFailures            uint64 `json:"reconnect_failures,omitempty"`
	ConsecutiveReconnectFailures uint32 `json:"consecutive_reconnect_failures,omitempty"`
	NextReconnect                int64  `json:"next_reconnect,omitempty"`
	TransferRx                   uint64 `json:"transfer_rx,omitempty"`
	TransferTx                   uint64 `json:"transfer_tx,omitempty"`
	FECPolicy                    string `json:"fec_policy,omitempty"`
}

type SetPeerEndpointRequest struct {
	PublicKey  string `json:"public_key"`
	Endpoint   string `json:"endpoint"`
	Generation uint64 `json:"generation"`
}

type PeerMutation struct {
	Operation           string   `json:"operation"`
	PublicKey           string   `json:"public_key"`
	PresharedKey        string   `json:"preshared_key,omitempty"`
	AllowedIPs          []string `json:"allowed_ips,omitempty"`
	Endpoint            string   `json:"endpoint,omitempty"`
	PersistentKeepalive uint16   `json:"persistent_keepalive"`
	FECPolicy           string   `json:"fec_policy,omitempty"`
}

type PeerSetRequest struct {
	TransactionID string         `json:"transaction_id"`
	Mutations     []PeerMutation `json:"mutations,omitempty"`
}

type Handler struct {
	Status           func() Status
	Events           func(string, uint64, int) (telemetry.SessionEventBatch, error)
	SetPeerEndpoint  func(SetPeerEndpointRequest) error
	RedialPeer       func(string) error
	Activate         func() error
	PreparePeerSet   func(PeerSetRequest) error
	CommitPeerSet    func(string) error
	RollbackPeerSet  func(string) error
	FinalizePeerSet  func(string) error
	FinalizeEndpoint func(string, uint64) error
}

// Client is the transport-independent core control surface used by quick.
// LocalClient currently carries it over a Unix socket or Windows named pipe.
type Client interface {
	Status() (Status, error)
	Events(eventStreamID string, afterSequence uint64, limit int) (telemetry.SessionEventBatch, error)
	SetPeerEndpoint(SetPeerEndpointRequest) error
	RedialPeer(publicKey string) error
	Activate() error
	PreparePeerSet(PeerSetRequest) error
	CommitPeerSet(transactionID string) error
	RollbackPeerSet(transactionID string) error
	FinalizePeerSet(transactionID string) error
	FinalizePeerEndpoint(publicKey string, generation uint64) error
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
	PeerSet         *PeerSetRequest         `json:"peer_set,omitempty"`
	TransactionID   string                  `json:"transaction_id,omitempty"`
	Generation      uint64                  `json:"generation,omitempty"`
	EventStreamID   string                  `json:"event_stream_id,omitempty"`
	AfterSequence   uint64                  `json:"after_sequence,omitempty"`
	EventLimit      int                     `json:"event_limit,omitempty"`
}

type response struct {
	Status *Status                      `json:"status,omitempty"`
	Events *telemetry.SessionEventBatch `json:"events,omitempty"`
	Error  string                       `json:"error,omitempty"`
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
	case "events":
		if handler.Events == nil {
			return response{Error: "events are not supported"}
		}
		events, err := handler.Events(req.EventStreamID, req.AfterSequence, req.EventLimit)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{Events: &events}
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
	case "prepare_peer_set":
		if req.PeerSet == nil {
			return response{Error: "peer_set payload is required"}
		}
		if handler.PreparePeerSet == nil {
			return response{Error: "prepare_peer_set is not supported"}
		}
		if err := handler.PreparePeerSet(*req.PeerSet); err != nil {
			return response{Error: err.Error()}
		}
		return response{}
	case "commit_peer_set":
		return dispatchPeerSetTransition(handler.CommitPeerSet, req.TransactionID, "commit_peer_set")
	case "rollback_peer_set":
		return dispatchPeerSetTransition(handler.RollbackPeerSet, req.TransactionID, "rollback_peer_set")
	case "finalize_peer_set":
		return dispatchPeerSetTransition(handler.FinalizePeerSet, req.TransactionID, "finalize_peer_set")
	case "finalize_peer_endpoint":
		if handler.FinalizeEndpoint == nil {
			return response{Error: "finalize_peer_endpoint is not supported"}
		}
		if err := handler.FinalizeEndpoint(req.PublicKey, req.Generation); err != nil {
			return response{Error: err.Error()}
		}
		return response{}
	default:
		return response{Error: fmt.Sprintf("unsupported control operation %q", req.Operation)}
	}
}

func dispatchPeerSetTransition(handler func(string) error, transactionID, operation string) response {
	if transactionID == "" {
		return response{Error: "transaction_id is required"}
	}
	if handler == nil {
		return response{Error: operation + " is not supported"}
	}
	if err := handler(transactionID); err != nil {
		return response{Error: err.Error()}
	}
	return response{}
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

func (c *LocalClient) Events(
	eventStreamID string,
	afterSequence uint64,
	limit int,
) (telemetry.SessionEventBatch, error) {
	var resp response
	err := c.call(request{
		Operation: "events", EventStreamID: eventStreamID,
		AfterSequence: afterSequence, EventLimit: limit,
	}, &resp)
	if err != nil {
		return telemetry.SessionEventBatch{}, err
	}
	if resp.Events == nil {
		return telemetry.SessionEventBatch{}, errors.New("control response did not include events")
	}
	return *resp.Events, nil
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

func (c *LocalClient) PreparePeerSet(peerSet PeerSetRequest) error {
	var resp response
	return c.call(request{Operation: "prepare_peer_set", PeerSet: &peerSet}, &resp)
}

func (c *LocalClient) CommitPeerSet(transactionID string) error {
	return c.peerSetTransition("commit_peer_set", transactionID)
}

func (c *LocalClient) RollbackPeerSet(transactionID string) error {
	return c.peerSetTransition("rollback_peer_set", transactionID)
}

func (c *LocalClient) FinalizePeerSet(transactionID string) error {
	return c.peerSetTransition("finalize_peer_set", transactionID)
}

func (c *LocalClient) FinalizePeerEndpoint(publicKey string, generation uint64) error {
	var resp response
	return c.call(request{
		Operation: "finalize_peer_endpoint", PublicKey: publicKey, Generation: generation,
	}, &resp)
}

func (c *LocalClient) peerSetTransition(operation, transactionID string) error {
	var resp response
	return c.call(request{Operation: operation, TransactionID: transactionID}, &resp)
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
