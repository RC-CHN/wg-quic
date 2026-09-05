package armorbind

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type Endpoint struct {
	owner           *Bind
	addr            netip.AddrPort
	receiveSequence uint64
	fecPolicy       string
	mu              sync.Mutex
	session         *session
	// route is the immutable per-datagram routing half of the endpoint:
	// replies reuse the configured endpoint itself, otherwise the
	// accept-time fallback. Writers publish before the endpoint becomes
	// visible to the receive path or while holding mu; the receive path
	// loads it lock-free per datagram.
	route               atomic.Pointer[endpointRoute]
	activated           bool
	retired             bool
	reconnectScheduled  bool
	reconnectCancel     context.CancelFunc
	reconnectGeneration uint64
	sessionGeneration   uint64
	consecutiveFailures uint32
	reconnectAttempts   uint64
	reconnectFailures   uint64
	nextReconnect       time.Time
	pendingReplacement  uint64
}

// endpointRoute is the immutable routing snapshot published on an Endpoint.
// It changes only at ParseEndpoint reconfiguration.
type endpointRoute struct {
	fallback   *Endpoint
	configured bool
}

// receiveEndpoint is an immutable packet snapshot. Keeping reconnection and
// configuration state on Endpoint avoids allocating that state for every
// received datagram. WireGuard may retain this snapshot after packet delivery.
type receiveEndpoint struct {
	owner           *Bind
	addr            netip.AddrPort
	receiveSequence uint64
	session         *session
	route           *endpointRoute
}

func (e *receiveEndpoint) ClearSrc()           {}
func (e *receiveEndpoint) SrcToString() string { return "" }
func (e *receiveEndpoint) DstToString() string { return e.addr.String() }
func (e *receiveEndpoint) DstToBytes() []byte {
	b, _ := e.addr.MarshalBinary()
	return b
}
func (e *receiveEndpoint) DstIP() netip.Addr       { return e.addr.Addr() }
func (e *receiveEndpoint) SrcIP() netip.Addr       { return netip.Addr{} }
func (e *receiveEndpoint) ReceiveSequence() uint64 { return e.receiveSequence }
func (e *receiveEndpoint) SessionID() uint64 {
	if e.session == nil {
		return 0
	}
	return e.session.id
}

func (e *receiveEndpoint) currentRoute() endpointRoute {
	if e.route != nil {
		return *e.route
	}
	return endpointRoute{}
}

func (e *Endpoint) isConfigured() bool {
	if r := e.route.Load(); r != nil {
		return r.configured
	}
	return false
}

func (e *Endpoint) currentRoute() endpointRoute {
	if r := e.route.Load(); r != nil {
		return *r
	}
	return endpointRoute{}
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
