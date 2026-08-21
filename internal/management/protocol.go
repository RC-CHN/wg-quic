// Package management implements the versioned, privileged local management
// protocol exposed by the wg-quic-quick supervisor.
package management

import (
	"context"
	"time"

	"github.com/RC-CHN/wg-quic/internal/reconcile"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

const ProtocolVersion = 1

const (
	OperationStatus            = "status"
	OperationReconcile         = "reconcile"
	OperationReload            = "reload"
	OperationTransactionStatus = "transaction_status"
	OperationRefreshEndpoints  = "refresh_endpoints"
)

type Request struct {
	ProtocolVersion      int      `json:"protocol_version"`
	Operation            string   `json:"operation"`
	Interface            string   `json:"interface"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	ExpectedEpoch        string   `json:"expected_epoch,omitempty"`
	ExpectedGeneration   uint64   `json:"expected_generation,omitempty"`
	RequestID            string   `json:"request_id,omitempty"`
	DeadlineUnixMillis   int64    `json:"deadline_unix_millis,omitempty"`
	CandidatePath        string   `json:"candidate_path,omitempty"`
	PublicKey            string   `json:"public_key,omitempty"`
}

func (r Request) Deadline() time.Time {
	if r.DeadlineUnixMillis == 0 {
		return time.Time{}
	}
	return time.UnixMilli(r.DeadlineUnixMillis)
}

type Status struct {
	ProtocolVersion   int               `json:"protocol_version"`
	Interface         string            `json:"interface"`
	State             string            `json:"state,omitempty"`
	ListenPort        uint16            `json:"listen_port,omitempty"`
	Carrier           string            `json:"carrier,omitempty"`
	FECMode           string            `json:"fec_mode,omitempty"`
	ObfsMode          string            `json:"obfs_mode,omitempty"`
	Addresses         []string          `json:"addresses,omitempty"`
	Stats             telemetry.Stats   `json:"stats"`
	SupervisorEpoch   string            `json:"supervisor_epoch"`
	DesiredGeneration uint64            `json:"desired_generation"`
	DesiredDigest     string            `json:"desired_digest"`
	CanonicalDigest   string            `json:"canonical_digest,omitempty"`
	CanonicalError    string            `json:"canonical_error,omitempty"`
	PersistentDrift   bool              `json:"persistent_drift"`
	CleanupPending    int               `json:"cleanup_pending_count"`
	Recovery          RecoveryStatus    `json:"recovery"`
	Capabilities      []string          `json:"capabilities"`
	Transaction       *reconcile.Result `json:"transaction,omitempty"`
	LastTransaction   *reconcile.Result `json:"last_transaction,omitempty"`
	Peers             []PeerStatus      `json:"peers,omitempty"`
}

type RecoveryStatus struct {
	State                    string `json:"state"`
	RetainedAmbiguousObjects int    `json:"retained_ambiguous_objects"`
	Message                  string `json:"message,omitempty"`
}

type PeerStatus struct {
	PublicKey                    string     `json:"public_key"`
	ConfiguredEndpoint           string     `json:"configured_endpoint,omitempty"`
	SelectedEndpoint             string     `json:"selected_endpoint,omitempty"`
	DNSCandidates                []string   `json:"dns_candidates,omitempty"`
	LastResolvedAt               *time.Time `json:"last_resolved_at,omitempty"`
	NextRefreshAt                *time.Time `json:"next_refresh_at,omitempty"`
	LastResolutionError          string     `json:"last_resolution_error,omitempty"`
	Session                      string     `json:"session,omitempty"`
	EndpointGeneration           uint64     `json:"endpoint_generation"`
	AuthenticatedGeneration      uint64     `json:"authenticated_endpoint_generation,omitempty"`
	AuthenticatedEndpoint        string     `json:"authenticated_endpoint,omitempty"`
	LatestHandshake              int64      `json:"latest_handshake,omitempty"`
	LastRx                       int64      `json:"last_rx,omitempty"`
	LastTx                       int64      `json:"last_tx,omitempty"`
	ReconnectAttempts            uint64     `json:"reconnect_attempts,omitempty"`
	ReconnectFailures            uint64     `json:"reconnect_failures,omitempty"`
	ConsecutiveReconnectFailures uint32     `json:"consecutive_reconnect_failures,omitempty"`
	TransferRx                   uint64     `json:"transfer_rx,omitempty"`
	TransferTx                   uint64     `json:"transfer_tx,omitempty"`
	FECPolicy                    string     `json:"fec_policy,omitempty"`
}

type Response struct {
	ProtocolVersion int                `json:"protocol_version"`
	Status          *Status            `json:"status,omitempty"`
	Result          *reconcile.Result  `json:"result,omitempty"`
	OperationResult *OperationResult   `json:"operation_result,omitempty"`
	Failure         *reconcile.Failure `json:"error,omitempty"`
}

type OperationResult struct {
	Operation string `json:"operation"`
	Interface string `json:"interface"`
	Peer      string `json:"peer,omitempty"`
}

type Handler interface {
	Handle(context.Context, Request) Response
}

type HandlerFunc func(context.Context, Request) Response

func (f HandlerFunc) Handle(ctx context.Context, request Request) Response {
	return f(ctx, request)
}

func ErrorResponse(code, message string, retryable bool) Response {
	return Response{
		ProtocolVersion: ProtocolVersion,
		Failure: &reconcile.Failure{
			Code: code, Stage: reconcile.StateValidating,
			Retryable: retryable, Message: message,
		},
	}
}
