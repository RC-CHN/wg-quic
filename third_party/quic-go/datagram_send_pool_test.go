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
