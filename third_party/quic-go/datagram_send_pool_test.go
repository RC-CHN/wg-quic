package quic

import "testing"

func TestDatagramSendBufferRecycles(t *testing.T) {
	buf := AcquireDatagramSendBuffer()
	if cap(buf) != DatagramSendBufferSize {
		t.Fatalf("acquired buffer capacity = %d, want %d", cap(buf), DatagramSendBufferSize)
	}
	first := &buf[0]
	releaseDatagramSendBuffer(buf[:100])
	again := AcquireDatagramSendBuffer()
	if &again[0] != first {
		t.Fatal("released buffer was not recycled")
	}
}

func TestReleaseDatagramSendBufferIgnoresForeignBuffers(t *testing.T) {
	foreign := make([]byte, DatagramSendBufferSize+1)
	releaseDatagramSendBuffer(foreign) // must not panic or pool
	small := make([]byte, 10)
	releaseDatagramSendBuffer(small)
}
