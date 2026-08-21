package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/reconcile"
)

func TestDecodeRequestIsBoundedAndRejectsUnknownFields(t *testing.T) {
	_, failure := decodeRequest(strings.NewReader(
		`{"protocol_version":1,"operation":"status","interface":"wg0","unknown":true}` + "\n",
	))
	if failure == nil || failure.Code != "decode_failed" {
		t.Fatalf("unknown-field failure = %#v", failure)
	}

	tooLarge := bytes.Repeat([]byte{'x'}, maxRequestSize+1)
	tooLarge[len(tooLarge)-1] = '\n'
	_, failure = decodeRequest(bytes.NewReader(tooLarge))
	if failure == nil || failure.Code != "request_too_large" {
		t.Fatalf("oversize failure = %#v", failure)
	}
}

func TestDecodeResponseIgnoresAdditiveOptionalFields(t *testing.T) {
	response, err := decodeResponse(strings.NewReader(
		`{"protocol_version":1,"future_status_field":{"enabled":true},"status":{"protocol_version":1,"interface":"wg0","supervisor_epoch":"epoch","desired_generation":1,"desired_digest":"sha256:test","persistent_drift":false,"cleanup_pending_count":0,"capabilities":[],"stats":{}}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if response.Status == nil || response.Status.Interface != "wg0" {
		t.Fatalf("decoded additive response = %#v", response)
	}
}

func TestValidateAndDispatchChecksProtocolAndOperation(t *testing.T) {
	var calls atomic.Int32
	handler := HandlerFunc(func(context.Context, Request) Response {
		calls.Add(1)
		return Response{}
	})
	response := validateAndDispatch(context.Background(), handler, Request{
		ProtocolVersion: 99, Operation: OperationStatus, Interface: "wg0",
	})
	if response.Failure == nil || response.Failure.Code != "unsupported_protocol" {
		t.Fatalf("protocol response = %#v", response)
	}
	response = validateAndDispatch(context.Background(), handler, Request{
		ProtocolVersion: ProtocolVersion, Operation: "destroy_everything", Interface: "wg0",
	})
	if response.Failure == nil || response.Failure.Code != "unsupported_operation" {
		t.Fatalf("operation response = %#v", response)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", calls.Load())
	}
}

func TestValidateAndDispatchBoundsRequiredCapabilities(t *testing.T) {
	handler := HandlerFunc(func(context.Context, Request) Response { return Response{} })
	request := Request{
		ProtocolVersion: ProtocolVersion, Operation: OperationStatus, Interface: "wg0",
		RequiredCapabilities: []string{"peer_reconcile_v1", "peer_reconcile_v1"},
	}
	response := validateAndDispatch(context.Background(), handler, request)
	if response.Failure == nil || response.Failure.Code != "validation_failed" {
		t.Fatalf("duplicate capability response = %#v", response)
	}
	request.RequiredCapabilities = make([]string, maxRequiredCapabilities+1)
	for index := range request.RequiredCapabilities {
		request.RequiredCapabilities[index] = "capability-" + string(rune('A'+index))
	}
	response = validateAndDispatch(context.Background(), handler, request)
	if response.Failure == nil || response.Failure.Code != "validation_failed" {
		t.Fatalf("oversize capability response = %#v", response)
	}
}

func TestServerServesOneVersionedRequest(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	server := &Server{
		ctx: context.Background(),
		handler: HandlerFunc(func(_ context.Context, request Request) Response {
			return Response{Status: &Status{
				Interface:         request.Interface,
				SupervisorEpoch:   "epoch",
				DesiredGeneration: 7,
				Capabilities:      []string{"management_protocol_v1"},
			}}
		}),
	}
	done := make(chan struct{})
	go func() {
		server.serve(serverConnection)
		_ = serverConnection.Close()
		close(done)
	}()
	if err := json.NewEncoder(clientConnection).Encode(Request{
		ProtocolVersion: ProtocolVersion,
		Operation:       OperationStatus,
		Interface:       "wg0",
	}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(clientConnection).Decode(&response); err != nil {
		t.Fatal(err)
	}
	<-done
	if response.ProtocolVersion != ProtocolVersion || response.Status == nil ||
		response.Status.Interface != "wg0" || response.Status.DesiredGeneration != 7 {
		t.Fatalf("response = %#v", response)
	}
}

func TestErrorResponseIsStructured(t *testing.T) {
	response := ErrorResponse("unsupported_capability", "not available", false)
	if response.ProtocolVersion != ProtocolVersion || response.Failure == nil ||
		response.Failure.Code != "unsupported_capability" ||
		response.Failure.Stage != reconcile.StateValidating {
		t.Fatalf("response = %#v", response)
	}
}
