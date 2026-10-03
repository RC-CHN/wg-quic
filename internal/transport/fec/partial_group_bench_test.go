package fec

import (
	"bytes"
	"fmt"
	"testing"

	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

// A flush deadline can close a group before it reaches the configured 32
// sources. Measure actual emitted bytes and datagrams, rather than inferring
// overhead from the controller's full-group parity target.
func BenchmarkEncoderPartialGroups(b *testing.B) {
	for _, parity := range []int{1, 4, 8} {
		for _, sources := range []int{1, 2, 4, 8, 32} {
			b.Run(fmt.Sprintf("target=%d/sources=%d", parity, sources), func(b *testing.B) {
				controller := NewController()
				controller.setParity(parity)
				encoder := NewEncoder(MaxDataShards, controller)
				frame := bytes.Repeat([]byte{42}, 1300)
				var wireBytes, datagrams int64
				record := func(packets [][]byte) {
					for _, packet := range packets {
						wireBytes += int64(len(packet))
						datagrams++
						quiccarrier.ReleaseDatagramSendBuffer(packet)
					}
				}
				b.ReportAllocs()
				b.SetBytes(int64(sources * len(frame)))
				b.ResetTimer()
				for range b.N {
					for range sources {
						packets, err := encoder.Add(frame)
						if err != nil {
							b.Fatal(err)
						}
						record(packets)
					}
					packets, err := encoder.Flush()
					if err != nil {
						b.Fatal(err)
					}
					record(packets)
				}
				b.ReportMetric(float64(wireBytes)/float64(b.N*sources*len(frame)), "wire/source")
				b.ReportMetric(float64(datagrams)/float64(b.N*sources), "datagrams/source")
			})
		}
	}
}
