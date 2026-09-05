package quic

import (
	"net"

	"github.com/quic-go/quic-go/internal/protocol"
)

type sender interface {
	Send(p *packetBuffer, gsoSize uint16, ecn protocol.ECN)
	SendProbe(*packetBuffer, net.Addr, packetInfo)
	Run() error
	WouldBlock() bool
	Available() <-chan struct{}
	Close()
}

type queueEntry struct {
	buf     *packetBuffer
	gsoSize uint16
	ecn     protocol.ECN
}

type sendQueue struct {
	queue             chan queueEntry
	closeCalled       chan struct{} // runStopped when Close() is called
	runStopped        chan struct{} // runStopped when the run loop returns
	available         chan struct{}
	conn              sendConn
	onPathMTUTooLarge func(protocol.ByteCount)
}

var _ sender = &sendQueue{}

const sendQueueCapacity = 8

func newSendQueue(conn sendConn, callbacks ...func(protocol.ByteCount)) sender {
	q := &sendQueue{
		conn:        conn,
		runStopped:  make(chan struct{}),
		closeCalled: make(chan struct{}),
		available:   make(chan struct{}, 1),
		queue:       make(chan queueEntry, sendQueueCapacity),
	}
	if len(callbacks) > 0 {
		q.onPathMTUTooLarge = callbacks[0]
	}
	return q
}

// Send sends out a packet. It's guaranteed to not block.
// Callers need to make sure that there's actually space in the send queue by calling WouldBlock.
// Otherwise Send will panic.
func (h *sendQueue) Send(p *packetBuffer, gsoSize uint16, ecn protocol.ECN) {
	select {
	case h.queue <- queueEntry{buf: p, gsoSize: gsoSize, ecn: ecn}:
		// clear available channel if we've reached capacity
		if len(h.queue) == sendQueueCapacity {
			select {
			case <-h.available:
			default:
			}
		}
	case <-h.runStopped:
	default:
		panic("sendQueue.Send would have blocked")
	}
}

func (h *sendQueue) SendProbe(p *packetBuffer, addr net.Addr, info packetInfo) {
	h.conn.WriteTo(p.Data, addr, info)
}

func (h *sendQueue) WouldBlock() bool {
	return len(h.queue) == sendQueueCapacity
}

func (h *sendQueue) Available() <-chan struct{} {
	return h.available
}

// coalesce combines already queued single-segment packets of equal size and
// ECN marking. DATAGRAM packets often leave unused space below the path MTU,
// ending the packer's GSO batch early. They can still share one socket write.
// The first packet's buffer bounds the batch; no allocation, padding or wait
// for more traffic is needed. Every packet has already passed the pacer.
// A dequeued packet that cannot join the batch is returned for the next write.
func (h *sendQueue) coalesce(e queueEntry) (queueEntry, queueEntry) {
	segmentSize := len(e.buf.Data)
	if e.gsoSize == 0 || segmentSize == 0 || segmentSize > int(e.gsoSize) {
		return e, queueEntry{}
	}
	// Freeze the burst size so a concurrent producer cannot extend the batch
	// indefinitely. At most sendQueueCapacity+1 segments are combined.
	for remaining := len(h.queue); remaining > 0 && len(e.buf.Data)+segmentSize <= cap(e.buf.Data); remaining-- {
		next := <-h.queue // Run is the sole reader; the sampled entries remain queued.
		if next.gsoSize == 0 || next.ecn != e.ecn ||
			len(next.buf.Data) != segmentSize || len(next.buf.Data) > int(next.gsoSize) {
			return e, next
		}
		e.buf.Data = append(e.buf.Data, next.buf.Data...)
		e.gsoSize = uint16(segmentSize)
		next.buf.Release()
	}
	return e, queueEntry{}
}

func (h *sendQueue) Run() error {
	var pending queueEntry
	defer func() {
		if pending.buf != nil {
			pending.buf.Release()
		}
		close(h.runStopped)
	}()
	var shouldClose bool
	for {
		if shouldClose && len(h.queue) == 0 && pending.buf == nil {
			return nil
		}
		var e queueEntry
		if pending.buf != nil {
			e, pending = pending, queueEntry{}
		} else {
			select {
			case <-h.closeCalled:
				h.closeCalled = nil // prevent this case from being selected again
				// Flush queued packets, including a pending packet from coalesce.
				shouldClose = true
				continue
			case e = <-h.queue:
			}
		}
		e, pending = h.coalesce(e)
		// UDP_SEGMENT adds kernel work even when it contains only one packet.
		// Keep ordinary writes for single packets and use GSO only for batches.
		if len(e.buf.Data) <= int(e.gsoSize) {
			e.gsoSize = 0
		}
		err := h.conn.Write(e.buf.Data, e.gsoSize, e.ecn)
		if isSendMsgSizeErr(err) && h.onPathMTUTooLarge != nil {
			size := protocol.ByteCount(len(e.buf.Data))
			if e.gsoSize > 0 {
				size = protocol.ByteCount(e.gsoSize)
			}
			h.onPathMTUTooLarge(size)
		}
		e.buf.Release()
		if err != nil && !isSendMsgSizeErr(err) {
			return err
		}
		select {
		case h.available <- struct{}{}:
		default:
		}
	}
}

func (h *sendQueue) Close() {
	close(h.closeCalled)
	// wait until the run loop returned
	<-h.runStopped
}
