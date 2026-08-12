package quic

import (
	"context"
	"net"
	"sync"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/utils/ringbuffer"
	"github.com/quic-go/quic-go/internal/wire"
)

const (
	// 32 datagrams is only ~340µs of buffer at gigabit rates; the wg-quic
	// carrier's session loop blocks in Add whenever quic-go's run loop is
	// busy packing, so give the queue enough depth to absorb that jitter.
	maxDatagramSendQueueLen = 128
	maxDatagramRcvQueueLen  = 128
)

type datagramReceiveBuffer [protocol.MaxPacketBufferSize]byte

var datagramReceiveBufferPool = sync.Pool{
	New: func() any {
		return new(datagramReceiveBuffer)
	},
}

// DatagramSendBufferSize is the capacity of pooled send buffers returned by
// AcquireDatagramSendBuffer.
const DatagramSendBufferSize = protocol.MaxPacketBufferSize

// datagramSendBufferPool recycles buffers handed to SendDatagramOwned. The
// packer returns each buffer once the frame has been serialized into a QUIC
// packet. The pool stores *[]byte so recycling needs only a capacity check; a
// capacity mismatch simply falls back to the GC.
var datagramSendBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, DatagramSendBufferSize)
		return &b
	},
}

// AcquireDatagramSendBuffer returns a pooled buffer for one owned outbound
// datagram. Callers must slice it from offset zero and pass the result to
// SendDatagramOwned; quic-go recycles the buffer after serialization.
func AcquireDatagramSendBuffer() []byte {
	p := datagramSendBufferPool.Get().(*[]byte)
	return *p
}

// releaseDatagramSendBuffer returns a buffer obtained from
// AcquireDatagramSendBuffer to the pool. The frame retains a stale reference
// in the sent-packet history for bookkeeping, but nothing ever reads the
// bytes again.
func releaseDatagramSendBuffer(data []byte) {
	if cap(data) != DatagramSendBufferSize {
		return
	}
	full := data[:DatagramSendBufferSize]
	datagramSendBufferPool.Put(&full)
}

// ReceivedDatagram owns a DATAGRAM payload returned by ReceiveDatagramOwned.
// Release must be called after the payload is no longer needed. Release is
// idempotent.
type ReceivedDatagram struct {
	Data       []byte
	RemoteAddr net.Addr
	buffer     *datagramReceiveBuffer
}

func (d *ReceivedDatagram) Release() {
	if d.buffer == nil {
		d.Data = nil
		return
	}
	d.Data = nil
	datagramReceiveBufferPool.Put(d.buffer)
	d.buffer = nil
}

type datagramQueue struct {
	sendMx    sync.Mutex
	sendQueue ringbuffer.RingBuffer[*wire.DatagramFrame]
	sent      chan struct{} // used to notify Add that a datagram was dequeued

	rcvMx    sync.Mutex
	rcvQueue ringbuffer.RingBuffer[ReceivedDatagram]
	rcvd     chan struct{} // used to notify Receive that a new datagram was received

	closeErr error
	closed   chan struct{}

	hasData func()

	logger utils.Logger
}

func newDatagramQueue(hasData func(), logger utils.Logger) *datagramQueue {
	queue := &datagramQueue{
		hasData: hasData,
		rcvd:    make(chan struct{}, 1),
		sent:    make(chan struct{}, 1),
		closed:  make(chan struct{}),
		logger:  logger,
	}
	queue.rcvQueue.Init(maxDatagramRcvQueueLen)
	return queue
}

// Add queues a new DATAGRAM frame for sending.
// Up to 32 DATAGRAM frames will be queued.
// Once that limit is reached, Add blocks until the queue size has reduced.
func (h *datagramQueue) Add(f *wire.DatagramFrame) error {
	h.sendMx.Lock()

	for {
		if h.sendQueue.Len() < maxDatagramSendQueueLen {
			h.sendQueue.PushBack(f)
			h.sendMx.Unlock()
			h.hasData()
			return nil
		}
		select {
		case <-h.sent: // drain the queue so we don't loop immediately
		default:
		}
		h.sendMx.Unlock()
		select {
		case <-h.closed:
			return h.closeErr
		case <-h.sent:
		}
		h.sendMx.Lock()
	}
}

// Peek gets the next DATAGRAM frame for sending.
// If actually sent out, Pop needs to be called before the next call to Peek.
func (h *datagramQueue) Peek() *wire.DatagramFrame {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	if h.sendQueue.Empty() {
		return nil
	}
	return h.sendQueue.PeekFront()
}

func (h *datagramQueue) Pop() {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	_ = h.sendQueue.PopFront()
	select {
	case h.sent <- struct{}{}:
	default:
	}
}

func (h *datagramQueue) Len() int {
	h.sendMx.Lock()
	defer h.sendMx.Unlock()
	return h.sendQueue.Len()
}

// HandleDatagramFrame handles a received DATAGRAM frame. The frame payload
// borrows the decrypted packet buffer, so copy it into a pooled queue buffer
// before packet processing returns.
func (h *datagramQueue) HandleDatagramFrame(f *wire.DatagramFrame) {
	h.HandleDatagramFrameFrom(f, nil)
}

// HandleDatagramFrameFrom preserves the authenticated QUIC packet's source
// address alongside its DATAGRAM payload. Applications that implement
// endpoint roaming need the packet path, which can differ from Conn.RemoteAddr
// until QUIC path validation completes.
func (h *datagramQueue) HandleDatagramFrameFrom(f *wire.DatagramFrame, remoteAddr net.Addr) {
	if len(f.Data) > protocol.MaxPacketBufferSize {
		if h.logger.Debug() {
			h.logger.Debugf("Discarding received DATAGRAM frame (%d bytes payload)", len(f.Data))
		}
		return
	}
	buffer := datagramReceiveBufferPool.Get().(*datagramReceiveBuffer)
	data := buffer[:len(f.Data)]
	copy(data, f.Data)
	datagram := ReceivedDatagram{
		Data:       data,
		RemoteAddr: cloneDatagramRemoteAddr(remoteAddr),
		buffer:     buffer,
	}
	var queued bool
	h.rcvMx.Lock()
	if h.rcvQueue.Len() < maxDatagramRcvQueueLen {
		h.rcvQueue.PushBack(datagram)
		queued = true
		select {
		case h.rcvd <- struct{}{}:
		default:
		}
	}
	h.rcvMx.Unlock()
	if !queued {
		datagram.Release()
		if h.logger.Debug() {
			h.logger.Debugf("Discarding received DATAGRAM frame (%d bytes payload)", len(f.Data))
		}
	}
}

func cloneDatagramRemoteAddr(remoteAddr net.Addr) net.Addr {
	udp, ok := remoteAddr.(*net.UDPAddr)
	if !ok || udp == nil {
		return remoteAddr
	}
	cloned := *udp
	cloned.IP = append(net.IP(nil), udp.IP...)
	return &cloned
}

// Receive gets a received DATAGRAM frame.
func (h *datagramQueue) Receive(ctx context.Context) ([]byte, error) {
	datagram, err := h.ReceiveOwned(ctx)
	if err != nil {
		return nil, err
	}
	data := append([]byte(nil), datagram.Data...)
	datagram.Release()
	return data, nil
}

func (h *datagramQueue) ReceiveOwned(ctx context.Context) (ReceivedDatagram, error) {
	for {
		h.rcvMx.Lock()
		if !h.rcvQueue.Empty() {
			datagram := h.rcvQueue.PopFront()
			h.rcvMx.Unlock()
			return datagram, nil
		}
		h.rcvMx.Unlock()
		select {
		case <-h.rcvd:
			continue
		case <-h.closed:
			return ReceivedDatagram{}, h.closeErr
		case <-ctx.Done():
			return ReceivedDatagram{}, ctx.Err()
		}
	}
}

func (h *datagramQueue) CloseWithError(e error) {
	h.rcvMx.Lock()
	for !h.rcvQueue.Empty() {
		datagram := h.rcvQueue.PopFront()
		datagram.Release()
	}
	h.rcvMx.Unlock()
	h.closeErr = e
	close(h.closed)
}
