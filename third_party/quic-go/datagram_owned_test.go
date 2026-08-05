package quic

import (
	"testing"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
	"github.com/quic-go/quic-go/internal/wire"

	"github.com/stretchr/testify/require"
)

func newDatagramSendTestConn(maxPayload protocol.ByteCount) *Conn {
	conn := &Conn{
		peerParams: &wire.TransportParameters{
			MaxDatagramFrameSize: wire.MaxDatagramSize,
		},
		version: protocol.Version1,
	}
	conn.maxPayloadSizeEstimate.Store(uint32(maxPayload))
	conn.datagramQueue = newDatagramQueue(func() {}, utils.DefaultLogger)
	return conn
}

func TestSendDatagramCopiesPayload(t *testing.T) {
	conn := newDatagramSendTestConn(1200)
	payload := []byte("ordinary datagram")

	require.NoError(t, conn.SendDatagram(payload))
	payload[0] = 'X'

	queued := conn.datagramQueue.Peek()
	require.NotNil(t, queued)
	require.Equal(t, []byte("ordinary datagram"), queued.Data)
	require.NotEqual(t, &payload[0], &queued.Data[0])
}

func TestSendDatagramOwnedTransfersPayload(t *testing.T) {
	conn := newDatagramSendTestConn(1200)
	payload := []byte("owned datagram")
	payloadStart := &payload[0]

	require.NoError(t, conn.SendDatagramOwned(payload))

	queued := conn.datagramQueue.Peek()
	require.NotNil(t, queued)
	require.Equal(t, payloadStart, &queued.Data[0])
	require.Equal(t, []byte("owned datagram"), queued.Data)
}

func TestSendDatagramOwnedKeepsOwnershipOnError(t *testing.T) {
	conn := newDatagramSendTestConn(4)
	payload := []byte("too large")

	var sizeErr *DatagramTooLargeError
	require.ErrorAs(t, conn.SendDatagramOwned(payload), &sizeErr)
	require.Nil(t, conn.datagramQueue.Peek())

	// An error leaves the slice with the caller.
	payload[0] = 'T'
	require.Equal(t, []byte("Too large"), payload)
}

func BenchmarkSendDatagramQueue(b *testing.B) {
	for _, test := range []struct {
		name  string
		owned bool
	}{
		{name: "copy"},
		{name: "owned", owned: true},
	} {
		b.Run(test.name, func(b *testing.B) {
			conn := newDatagramSendTestConn(1350)
			payload := make([]byte, 1200)
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var err error
				if test.owned {
					err = conn.SendDatagramOwned(payload)
				} else {
					err = conn.SendDatagram(payload)
				}
				if err != nil {
					b.Fatal(err)
				}
				conn.datagramQueue.Pop()
			}
		})
	}
}
