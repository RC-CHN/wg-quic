//go:build linux

package quic

import (
	"testing"
	"testing/synctest"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"
)

func TestSendQueueReportsPathMTUReduction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mockCtrl := gomock.NewController(t)
		conn := NewMockSendConn(mockCtrl)
		reported := make(chan protocol.ByteCount, 1)
		queue := newSendQueue(conn, func(size protocol.ByteCount) {
			reported <- size
		})

		conn.EXPECT().
			Write(gomock.Any(), uint16(0), protocol.ECNNon).
			Return(unix.EMSGSIZE)
		queue.Send(getPacketWithContents(make([]byte, 1280)), 0, protocol.ECNNon)

		done := make(chan error, 1)
		go func() { done <- queue.Run() }()
		synctest.Wait()

		select {
		case size := <-reported:
			require.Equal(t, protocol.ByteCount(1280), size)
		default:
			t.Fatal("path MTU reduction was not reported")
		}

		queue.Close()
		synctest.Wait()
		require.NoError(t, <-done)
	})
}
