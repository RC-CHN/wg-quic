package armorbind

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

type Endpoint struct {
	owner           *Bind
	addr            netip.AddrPort
	receiveSequence uint64
	fecPolicy       string
	mu              sync.Mutex
	session         *session
	fallback        *Endpoint

	configured          bool
	activated           bool
	retired             bool
	reconnectScheduled  bool
	reconnectCancel     context.CancelFunc
	reconnectGeneration uint64
	consecutiveFailures uint32
	reconnectAttempts   uint64
	reconnectFailures   uint64
	nextReconnect       time.Time
}

func (e *Endpoint) cancelReconnectLocked() {
	if e.reconnectCancel != nil {
		e.reconnectCancel()
	}
	e.reconnectGeneration++
	e.reconnectScheduled = false
	e.reconnectCancel = nil
	e.nextReconnect = time.Time{}
}

func (e *Endpoint) ClearSrc()           {}
func (e *Endpoint) SrcToString() string { return "" }
func (e *Endpoint) DstToString() string { return e.addr.String() }
func (e *Endpoint) DstToBytes() []byte {
	b, _ := e.addr.MarshalBinary()
	return b
}
func (e *Endpoint) DstIP() netip.Addr       { return e.addr.Addr() }
func (e *Endpoint) SrcIP() netip.Addr       { return netip.Addr{} }
func (e *Endpoint) ReceiveSequence() uint64 { return e.receiveSequence }
func (e *Endpoint) SessionID() uint64 {
	if e.session == nil {
		return 0
	}
	return e.session.id
}
