package quic

import "testing"

func TestDatagramSendBufferRecycles(t *testing.T) {
	// sync.Pool may drop entries at any GC, so one release/acquire pair
	// cannot prove recycling; require at least one recycle within a bounded
	// number of rounds.
	for range 8 {
		buf := AcquireDatagramSendBuffer()
		if cap(buf) != DatagramSendBufferSize {
			t.Fatalf("acquired buffer capacity = %d, want %d", cap(buf), DatagramSendBufferSize)
		}
		first := &buf[0]
		releaseDatagramSendBuffer(buf[:100])
		if again := AcquireDatagramSendBuffer(); &again[0] == first {
			return
		}
	}
	t.Fatal("released buffer was never recycled")
}

func TestReleaseDatagramSendBufferIgnoresForeignBuffers(t *testing.T) {
	foreign := make([]byte, DatagramSendBufferSize+1)
	releaseDatagramSendBuffer(foreign) // must not panic or pool
	small := make([]byte, 10)
	releaseDatagramSendBuffer(small)
}

func TestDatagramSendPoolKeepsOutstandingBuffersDistinct(t *testing.T) {
	first := AcquireDatagramSendBuffer()
	second := AcquireDatagramSendBuffer()
	defer releaseDatagramSendBuffer(first)
	defer releaseDatagramSendBuffer(second)
	first[0], second[0] = 1, 2
	if first[0] != 1 || len(first) != DatagramSendBufferSize || len(second) != DatagramSendBufferSize {
		t.Fatal("outstanding buffers alias or have the wrong length")
	}
	// A zero-length view still owns the complete pooled backing array.
	third := AcquireDatagramSendBuffer()
	releaseDatagramSendBuffer(third[:0])
}

func BenchmarkDatagramSendBufferPool(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		buf := AcquireDatagramSendBuffer()
		buf[0] = 1
		releaseDatagramSendBuffer(buf[:1200])
	}
}
