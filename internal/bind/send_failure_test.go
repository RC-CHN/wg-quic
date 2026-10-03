package armorbind

import (
	"bytes"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

func TestBindRecoversAfterDatagramSendFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ReconnectMin = 10 * time.Millisecond
	cfg.ReconnectMax = 20 * time.Millisecond
	cfg.ReconnectJitter = func(delay time.Duration) time.Duration { return delay }
	a, b := New(cfg), New(cfg)
	if _, _, err := a.Open(0); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	receive, port, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	endpoint, err := a.ParseEndpoint(net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("before send failure")
	if err := a.Send([][]byte{payload}, endpoint); err != nil {
		t.Fatal(err)
	}
	receiveOne(t, receive[0])
	ep := endpoint.(*Endpoint)
	ep.mu.Lock()
	failed := ep.session
	ep.mu.Unlock()
	if failed == nil {
		t.Fatal("session not established")
	}
	// Force a non-terminal QUIC send error. An MTU/encoding send failure
	// doesn't close the transport's receive half; the bind must retire the
	// failed send worker itself, otherwise keepalives can preserve a session
	// which only receives and silently fills its outgoing queue forever.
	failed.control <- make([]byte, maxDatagramSize)
	select {
	case <-failed.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("send failure left the receive loop and dead session alive")
	}
	waitForCondition(t, "redial after send failure", func() bool {
		ep.mu.Lock()
		defer ep.mu.Unlock()
		return ep.session != nil && ep.session != failed && !ep.session.closed.Load()
	})
	payload = []byte("after automatic recovery")
	if err := a.Send([][]byte{payload}, endpoint); err != nil {
		t.Fatal(err)
	}
	got, _ := receiveOne(t, receive[0])
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload after recovery = %q, want %q", got, payload)
	}
	closed, _ := a.RecentSessionTelemetry()
	for _, entry := range closed {
		if entry.SessionID == failed.id {
			if entry.CloseReason != telemetry.SessionCloseTransportError || entry.LastError == "" {
				t.Fatalf("send failure diagnostic missing: %+v", entry)
			}
			return
		}
	}
	t.Fatal("failed session not retained in diagnostics")
}
