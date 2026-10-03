package armorbind

import (
	"bytes"
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExpiredDataIsDiscardedBeforeFECWithoutClosingSession(t *testing.T) {
	a, b := New(DefaultConfig()), New(DefaultConfig())
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
	if err := a.Send([][]byte{[]byte("establish")}, endpoint); err != nil {
		t.Fatal(err)
	}
	receiveOne(t, receive[0])
	ep := endpoint.(*Endpoint)
	ep.mu.Lock()
	s := ep.session
	ep.mu.Unlock()
	stale := quiccarrier.AcquireDatagramSendBuffer(frameHeaderSize + 256)
	copy(stale[frameHeaderSize:], bytes.Repeat([]byte("x"), 256))
	if !s.reserveSendBytes(len(stale)) {
		t.Fatal("cannot enqueue test packet")
	}
	s.send <- outboundPacket{preparedFrame: stale, id: a.nextPacket.Add(1), queuedAt: time.Now().Add(-3 * time.Second)}
	fresh := []byte("fresh packet after stale queued data")
	if err := a.Send([][]byte{fresh}, endpoint); err != nil {
		t.Fatal(err)
	}
	got, _ := receiveOne(t, receive[0])
	if !bytes.Equal(got, fresh) {
		t.Fatalf("delivered expired packet: %q", got)
	}
	if s.stats.queueDrops.Load() != 1 || s.sendBytes.Load() != 0 || s.ctx.Err() != nil {
		t.Fatalf("drop must release admission and retain session: drops=%d bytes=%d err=%v", s.stats.queueDrops.Load(), s.sendBytes.Load(), s.ctx.Err())
	}
}

func TestSendBudgetTracksCapacityAndBoundsConcurrentAdmission(t *testing.T) {
	s := &session{send: make(chan outboundPacket, 1024)}
	s.updateSendBudget(100_000_000, 110_000_000, false)
	fast := s.sendBudgetBytes.Load()
	s.updateSendBudget(1_000_000, 1_100_000, false)
	if s.sendBudgetBytes.Load() >= fast || s.sendBudgetBytes.Load() > 10_000 {
		t.Fatal("bandwidth drop did not shrink the admission budget")
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if s.reserveSendBytes(1500) {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 4 || s.sendBytes.Load() != 6000 {
		t.Fatalf("concurrent admission overcommitted: packets=%d bytes=%d", accepted.Load(), s.sendBytes.Load())
	}
	for range 4 {
		s.releaseSendBytes(outboundPacket{queuedAt: time.Now(), preparedFrame: make([]byte, 1500)})
	}
	if s.sendBytes.Load() != 0 || !s.reserveSendBytes(65536) {
		t.Fatal("draining must allow a complete large datagram to make progress")
	}
}

func TestSendBudgetProtectsSlowSerializationAndPriorityAccounting(t *testing.T) {
	s := &session{send: make(chan outboundPacket, 1024)}
	s.updateSendBudget(32_000, 40_000, false)
	if s.sendMaxAge() < time.Second {
		t.Fatal("slow serialization needs more than a fixed 100 ms deadline")
	}
	s.releaseSendBytes(outboundPacket{preparedFrame: make([]byte, 100)})
	if s.sendBytes.Load() != 0 {
		t.Fatal("priority frames must not release data admission")
	}
	s.updateSendBudget(100_000_000, 110_000_000, false)
	if s.sendMaxAge() != 100*time.Millisecond {
		t.Fatal("queue age did not recover with bandwidth")
	}
}
