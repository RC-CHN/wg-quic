package quic

import (
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"
	"github.com/stretchr/testify/require"
	"testing"
	"testing/synctest"
)

func TestDatagramBudgetRespondsToCapacity(t *testing.T) {
	require.Equal(t, 4800, datagramSendBudget(1_000_000, 1_100_000))
	require.Equal(t, 62500, datagramSendBudget(100_000_000, 110_000_000))
	require.Equal(t, 4800, datagramSendBudget(100_000_000, 1_000_000))
	require.Equal(t, maxDatagramSendQueueLen*DatagramSendBufferSize, datagramSendBudget(0, 0))
}

func TestDatagramBudgetBackpressuresAndDrainsAfterShrink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDatagramQueue(func() {}, utils.DefaultLogger)
		budget := 10000
		q.sendBudget = func() int { return budget }
		for range 8 {
			require.NoError(t, q.Add(&wire.DatagramFrame{Data: make([]byte, 1200)}))
		}
		budget = 4800
		done := make(chan error, 1)
		go func() { done <- q.Add(&wire.DatagramFrame{Data: make([]byte, 1200)}) }()
		synctest.Wait()
		for range 4 {
			q.Pop()
		}
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("admitted above reduced budget")
		default:
		}
		q.Pop()
		synctest.Wait()
		require.NoError(t, <-done)
		require.Equal(t, 4800, q.sendBytes)
		q.CloseWithError(assertBudgetClosed{})
		require.Zero(t, q.sendBytes)
	})
}

type assertBudgetClosed struct{}

func (assertBudgetClosed) Error() string { return "closed" }
