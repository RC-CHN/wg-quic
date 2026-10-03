package quic

import (
	"context"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
	"testing"
	"testing/synctest"
	"time"
)

func TestQueueObservationIncludesReservedControlAndResetsWhenDrained(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDatagramQueue(func() {}, utils.DefaultLogger)
		q.sendBudget = func() int { return 4800 }
		require.NoError(t, q.Add(&wire.DatagramFrame{Data: []byte("data")}))
		time.Sleep(25 * time.Millisecond)
		require.NoError(t, q.addContext(context.Background(), &wire.DatagramFrame{Data: []byte("ctrl")}, true))
		bytes, budget, age := q.SendQueueObservation()
		require.Equal(t, 8, bytes)
		require.Equal(t, 4800, budget)
		require.Equal(t, 25*time.Millisecond, age)
		q.Pop()
		q.Pop()
		bytes, _, age = q.SendQueueObservation()
		require.Zero(t, bytes)
		require.Zero(t, age)
	})
}

func TestPriorityDatagramPassesBlockedDataAndPreservesPinnedPeek(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDatagramQueue(func() {}, utils.DefaultLogger)
		q.sendBudget = func() int { return 4 }
		data := &wire.DatagramFrame{Data: []byte("data")}
		require.NoError(t, q.Add(data))
		require.Same(t, data, q.Peek())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- q.addContext(ctx, &wire.DatagramFrame{Data: []byte("next")}, false) }()
		synctest.Wait()
		priority := &wire.DatagramFrame{Data: []byte("control")}
		require.NoError(t, q.addContext(ctx, priority, true))
		require.Same(t, data, q.Peek(), "arrival must not change an already selected packet")
		q.Pop()
		synctest.Wait()
		require.NoError(t, <-done)
		require.Same(t, priority, q.Peek(), "control must precede pending bulk data")
		q.Pop()
		require.Equal(t, "next", string(q.Peek().Data))
		q.Pop()
		require.Zero(t, q.sendBytes)
	})
}

func TestDatagramAdmissionCancellationDoesNotCloseQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, priority := range []bool{false, true} {
			q := newDatagramQueue(func() {}, utils.DefaultLogger)
			capacity := maxDatagramSendQueueLen
			if priority {
				capacity = 8
			}
			for range capacity {
				require.NoError(t, q.addContext(context.Background(), &wire.DatagramFrame{Data: []byte("x")}, priority))
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			frame := &wire.DatagramFrame{Data: []byte("retained")}
			go func() { done <- q.addContext(ctx, frame, priority) }()
			synctest.Wait()
			cancel()
			synctest.Wait()
			require.ErrorIs(t, <-done, context.Canceled)
			require.Equal(t, "retained", string(frame.Data))
			q.Pop()
			require.NoError(t, q.addContext(context.Background(), frame, priority))
		}
	})
}
