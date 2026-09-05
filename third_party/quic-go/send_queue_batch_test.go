package quic

import (
	"bytes"
	"testing"
	"testing/synctest"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSendQueueCoalescesWithoutChangingDatagrams(t *testing.T) {
	type input struct {
		size int
		gso  uint16
		ecn  protocol.ECN
	}
	for _, tc := range []struct {
		name   string
		inputs []input
		small  bool
		writes int
	}{
		{"equal packets", []input{{1300, 1452, protocol.ECT1}, {1300, 1452, protocol.ECT1}, {1300, 1452, protocol.ECT1}}, false, 1},
		{"different lengths", []input{{1000, 1452, protocol.ECT1}, {1100, 1452, protocol.ECT1}, {1000, 1452, protocol.ECT1}, {1000, 1452, protocol.ECT1}}, false, 3},
		{"different ECN", []input{{1300, 1452, protocol.ECT1}, {1300, 1452, protocol.ECT1}, {1300, 1452, protocol.ECNNon}, {1300, 1452, protocol.ECNNon}}, false, 2},
		{"GSO disabled", []input{{1300, 0, protocol.ECT1}, {1300, 0, protocol.ECT1}, {1300, 0, protocol.ECT1}}, false, 3},
		{"GSO boundary", []input{{1300, 1452, protocol.ECT1}, {1300, 0, protocol.ECT1}, {1300, 1452, protocol.ECT1}}, false, 3},
		{"existing batch", []input{{2000, 1000, protocol.ECT1}, {1000, 1452, protocol.ECT1}}, false, 2},
		{"existing batch after single", []input{{1000, 1452, protocol.ECT1}, {2000, 1000, protocol.ECT1}}, false, 2},
		{"buffer capacity", []input{{1400, 1452, protocol.ECT1}, {1400, 1452, protocol.ECT1}}, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				type datagram struct {
					data []byte
					ecn  protocol.ECN
				}
				split := func(data []byte, gso uint16, ecn protocol.ECN) []datagram {
					var packets []datagram
					for len(data) > 0 {
						n := len(data)
						if gso > 0 {
							n = min(n, int(gso))
						}
						packets = append(packets, datagram{bytes.Clone(data[:n]), ecn})
						data = data[n:]
					}
					return packets
				}
				conn := NewMockSendConn(gomock.NewController(t))
				q := newSendQueue(conn)
				var want, got []datagram
				var buffers []*packetBuffer
				for i, in := range tc.inputs {
					buf := getPacketBuffer()
					if !tc.small {
						buf.Release()
						buf = getLargePacketBuffer()
					}
					buf.Data = append(buf.Data, bytes.Repeat([]byte{byte(i + 1)}, in.size)...)
					want = append(want, split(buf.Data, in.gso, in.ecn)...)
					buffers = append(buffers, buf)
					q.Send(buf, in.gso, in.ecn)
				}
				conn.EXPECT().Write(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
					func(data []byte, gso uint16, ecn protocol.ECN) error {
						got = append(got, split(data, gso, ecn)...)
						return nil
					},
				).Times(tc.writes)
				done := make(chan error, 1)
				go func() { done <- q.Run() }()
				// Close must drain the batch, an incompatible pending packet,
				// and all remaining queued packets in their original order.
				q.Close()
				require.NoError(t, <-done)
				require.Equal(t, want, got)
				for _, buf := range buffers {
					require.Zero(t, buf.refCount)
				}
			})
		})
	}
}

func TestSendQueueBatchWriteErrorReleasesPendingPacket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := NewMockSendConn(gomock.NewController(t))
		q := newSendQueue(conn)
		var buffers []*packetBuffer
		for _, size := range []int{1000, 1000, 1100} {
			buf := getLargePacketBuffer()
			buf.Data = append(buf.Data, make([]byte, size)...)
			buffers = append(buffers, buf)
			q.Send(buf, 1452, protocol.ECT1)
		}
		conn.EXPECT().Write(gomock.Len(2000), uint16(1000), protocol.ECT1).Return(errNothingToPack)
		require.ErrorIs(t, q.Run(), errNothingToPack)
		for _, buf := range buffers {
			require.Zero(t, buf.refCount)
		}
		q.Close()
	})
}
