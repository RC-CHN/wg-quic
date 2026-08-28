package quic

import (
	"context"
	"net"
	"testing"
	"testing/synctest"

	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatagramQueuePeekAndPop(t *testing.T) {
	var queued []struct{}
	queue := newDatagramQueue(func() { queued = append(queued, struct{}{}) }, utils.DefaultLogger)
	require.Nil(t, queue.Peek())
	require.Empty(t, queued)
	require.NoError(t, queue.Add(&wire.DatagramFrame{Data: []byte("foo")}))
	require.Equal(t, 1, queue.Len())
	require.Len(t, queued, 1)
	require.Equal(t, &wire.DatagramFrame{Data: []byte("foo")}, queue.Peek())
	// calling peek again returns the same datagram
	require.Equal(t, &wire.DatagramFrame{Data: []byte("foo")}, queue.Peek())
	queue.Pop()
	require.Zero(t, queue.Len())
	require.Nil(t, queue.Peek())
}

func TestDatagramQueueSendQueueLength(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue := newDatagramQueue(func() {}, utils.DefaultLogger)

		for range maxDatagramSendQueueLen {
			require.NoError(t, queue.Add(&wire.DatagramFrame{Data: []byte{0}}))
		}
		errChan := make(chan error, 1)
		go func() { errChan <- queue.Add(&wire.DatagramFrame{Data: []byte("foobar")}) }()

		synctest.Wait()

		select {
		case <-errChan:
			t.Fatal("expected to not receive error")
		default:
		}

		// peeking doesn't remove the datagram from the queue...
		require.NotNil(t, queue.Peek())
		synctest.Wait()
		select {
		case <-errChan:
			t.Fatal("expected to not receive error")
		default:
		}

		// ...but popping does
		queue.Pop()
		synctest.Wait()
		select {
		case err := <-errChan:
			require.NoError(t, err)
		default:
			t.Fatal("timeout")
		}
		// pop all the remaining datagrams
		for range maxDatagramSendQueueLen - 1 {
			queue.Pop()
		}
		f := queue.Peek()
		require.NotNil(t, f)
		require.Equal(t, &wire.DatagramFrame{Data: []byte("foobar")}, f)
	})
}

func TestDatagramQueueReceive(t *testing.T) {
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)

	// receive frames that were received earlier
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("foo")})
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("bar")})
	data, err := queue.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), data)
	data, err = queue.Receive(context.Background())
	require.NoError(t, err)
	require.Equal(t, []byte("bar"), data)
}

func TestDatagramQueueReceiveDropsCounted(t *testing.T) {
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)

	// fill the receive queue
	for range defaultMaxDatagramRcvQueueLen {
		queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte{0}})
	}
	require.Equal(t, defaultMaxDatagramRcvQueueLen, queue.RcvQueueLen())
	require.Equal(t, uint64(defaultMaxDatagramRcvQueueLen), queue.RcvQueueHighWater())
	require.Zero(t, queue.RcvQueueDrops())

	// further datagrams are released and counted
	for range 3 {
		queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte{0}})
	}
	require.Equal(t, uint64(3), queue.RcvQueueDrops())
	require.Equal(t, defaultMaxDatagramRcvQueueLen, queue.RcvQueueLen())

	// draining resets neither the high-water mark nor the drop counter
	datagram, err := queue.ReceiveOwned(context.Background())
	require.NoError(t, err)
	datagram.Release()
	require.Equal(t, defaultMaxDatagramRcvQueueLen-1, queue.RcvQueueLen())
	require.Equal(t, uint64(defaultMaxDatagramRcvQueueLen), queue.RcvQueueHighWater())
	require.Equal(t, uint64(3), queue.RcvQueueDrops())

	// a freed slot queues again without a drop
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte{0}})
	require.Equal(t, uint64(3), queue.RcvQueueDrops())
	require.Equal(t, defaultMaxDatagramRcvQueueLen, queue.RcvQueueLen())
}

func TestDatagramRcvQueueCapacityConfigurable(t *testing.T) {
	SetMaxDatagramRcvQueueLen(4)
	defer SetMaxDatagramRcvQueueLen(0)
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)

	for range 6 {
		queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte{0}})
	}
	require.Equal(t, 4, queue.RcvQueueLen())
	require.Equal(t, uint64(4), queue.RcvQueueHighWater())
	require.Equal(t, uint64(2), queue.RcvQueueDrops())

	// a queue keeps the capacity frozen at creation across a reconfigure
	SetMaxDatagramRcvQueueLen(8)
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte{0}})
	require.Equal(t, 4, queue.RcvQueueLen())
	require.Equal(t, uint64(3), queue.RcvQueueDrops())
}

func TestDatagramQueueReceiveOwnedUsesIndependentPooledBuffer(t *testing.T) {
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)
	payload := []byte("owned receive payload")
	payloadStart := &payload[0]
	frame := &wire.DatagramFrame{Data: payload}

	queue.HandleDatagramFrame(frame)
	payload[0] = 'X'

	datagram, err := queue.ReceiveOwned(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, payloadStart, &datagram.Data[0])
	require.Equal(t, []byte("owned receive payload"), datagram.Data)
	require.NotNil(t, datagram.buffer)
	datagram.Release()
	require.Nil(t, datagram.Data)
	require.Nil(t, datagram.buffer)
	datagram.Release()
}

func TestDatagramQueueReceiveOwnedPreservesRemoteAddress(t *testing.T) {
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)
	remote := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 20), Port: 52821}
	queue.HandleDatagramFrameFrom(
		&wire.DatagramFrame{Data: []byte("migrated path")},
		remote,
	)
	remote.IP[0] = 203
	remote.Port = 1

	datagram, err := queue.ReceiveOwned(context.Background())
	require.NoError(t, err)
	require.Equal(t, "198.51.100.20:52821", datagram.RemoteAddr.String())
	datagram.Release()
}

func TestDatagramQueueReceiveReturnsCallerOwnedCopy(t *testing.T) {
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("first payload")})

	first, err := queue.Receive(context.Background())
	require.NoError(t, err)
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("second payload")})
	second, err := queue.Receive(context.Background())
	require.NoError(t, err)

	require.Equal(t, []byte("first payload"), first)
	require.Equal(t, []byte("second payload"), second)
}

func TestDatagramQueueReceiveBlocking(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue := newDatagramQueue(func() {}, utils.DefaultLogger)

		// block until a new frame is received
		type result struct {
			data []byte
			err  error
		}
		resultChan := make(chan result, 1)
		go func() {
			data, err := queue.Receive(context.Background())
			resultChan <- result{data, err}
		}()

		synctest.Wait()

		select {
		case <-resultChan:
			t.Fatal("expected to not receive result")
		default:
		}
		queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("foobar")})
		synctest.Wait()
		select {
		case result := <-resultChan:
			require.NoError(t, result.err)
			require.Equal(t, []byte("foobar"), result.data)
		default:
			t.Fatal("should have received a datagram frame")
		}

		// unblock when the context is canceled
		ctx, cancel := context.WithCancel(context.Background())
		errChan := make(chan error, 1)
		go func() {
			_, err := queue.Receive(ctx)
			errChan <- err
		}()

		synctest.Wait()
		select {
		case <-errChan:
			t.Fatal("expected to not receive error")
		default:
		}

		cancel()
		synctest.Wait()

		select {
		case err := <-errChan:
			require.ErrorIs(t, err, context.Canceled)
		default:
			t.Fatal("should have received a context canceled error")
		}
	})
}

func TestDatagramQueueClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		queue := newDatagramQueue(func() {}, utils.DefaultLogger)

		for range maxDatagramSendQueueLen {
			require.NoError(t, queue.Add(&wire.DatagramFrame{Data: []byte{0}}))
		}
		errChan1 := make(chan error, 1)
		go func() { errChan1 <- queue.Add(&wire.DatagramFrame{Data: []byte("foobar")}) }()
		errChan2 := make(chan error, 1)
		go func() {
			_, err := queue.Receive(context.Background())
			errChan2 <- err
		}()

		queue.CloseWithError(assert.AnError)
		synctest.Wait()

		select {
		case err := <-errChan1:
			require.ErrorIs(t, err, assert.AnError)
		default:
			t.Fatal("should have received an error")
		}

		select {
		case err := <-errChan2:
			require.ErrorIs(t, err, assert.AnError)
		default:
			t.Fatal("should have received an error")
		}
	})
}

func TestDatagramQueueCloseDrainsReceiveBuffers(t *testing.T) {
	queue := newDatagramQueue(func() {}, utils.DefaultLogger)
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("foo")})
	queue.HandleDatagramFrame(&wire.DatagramFrame{Data: []byte("bar")})
	require.Equal(t, 2, queue.rcvQueue.Len())

	queue.CloseWithError(assert.AnError)

	require.Zero(t, queue.rcvQueue.Len())
}

func BenchmarkDatagramReceiveQueue(b *testing.B) {
	payload := make([]byte, 1200)
	b.Run("caller-owned-copy", func(b *testing.B) {
		queue := newDatagramQueue(func() {}, utils.DefaultLogger)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			queue.HandleDatagramFrame(&wire.DatagramFrame{Data: payload})
			data, err := queue.Receive(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			if len(data) != len(payload) {
				b.Fatalf("received %d bytes", len(data))
			}
		}
	})
	b.Run("owned-pooled", func(b *testing.B) {
		queue := newDatagramQueue(func() {}, utils.DefaultLogger)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			queue.HandleDatagramFrame(&wire.DatagramFrame{Data: payload})
			datagram, err := queue.ReceiveOwned(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			if len(datagram.Data) != len(payload) {
				b.Fatalf("received %d bytes", len(datagram.Data))
			}
			datagram.Release()
		}
	})
}
