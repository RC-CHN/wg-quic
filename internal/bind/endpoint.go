package armorbind

import (
	"net/netip"
	"sync"
)

type Endpoint struct {
	owner   *Bind
	addr    netip.AddrPort
	mu      sync.Mutex
	session *session
}

func (e *Endpoint) ClearSrc()           {}
func (e *Endpoint) SrcToString() string { return "" }
func (e *Endpoint) DstToString() string { return e.addr.String() }
func (e *Endpoint) DstToBytes() []byte {
	b, _ := e.addr.MarshalBinary()
	return b
}
func (e *Endpoint) DstIP() netip.Addr { return e.addr.Addr() }
func (e *Endpoint) SrcIP() netip.Addr { return netip.Addr{} }
