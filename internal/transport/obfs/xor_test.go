package obfs

import (
	"bytes"
	"fmt"
	"testing"
)

func TestXORWords(t *testing.T) {
	var stream [32]byte
	for i := range stream {
		stream[i] = byte(i*7 + 13)
	}
	// Include every tail length, unaligned slices, empty and maximum UDP
	// payloads. Check guard bytes as well as exact in-place decoding.
	for _, size := range append(xorTailSizes(), 1300, 9000, maxUDPPayload-SalamanderHeaderSize) {
		for _, offset := range []int{0, 1, 7, 15} {
			src := make([]byte, size+offset)[offset:]
			want := make([]byte, size)
			for i := range src {
				src[i] = byte(i*11 + 3)
				want[i] = src[i] ^ stream[i%len(stream)]
			}
			storage := bytes.Repeat([]byte{0xa5}, offset+size+32)
			xorWords(storage[offset:offset+size], src, &stream)
			if !bytes.Equal(storage[offset:offset+size], want) {
				t.Fatalf("size=%d offset=%d: incorrect XOR", size, offset)
			}
			if !bytes.Equal(storage[:offset], bytes.Repeat([]byte{0xa5}, offset)) ||
				!bytes.Equal(storage[offset+size:], bytes.Repeat([]byte{0xa5}, 32)) {
				t.Fatalf("size=%d offset=%d: wrote outside output", size, offset)
			}
			xorWords(src, src, &stream)
			if !bytes.Equal(src, want) {
				t.Fatalf("size=%d offset=%d: incorrect in-place XOR", size, offset)
			}
		}
	}
}

func xorTailSizes() []int {
	sizes := make([]int, 97)
	for i := range sizes {
		sizes[i] = i
	}
	return sizes
}

func TestDigestPairReuseMatchesIndependentDerivation(t *testing.T) {
	key := Key{3, 1, 4, 1, 5}
	pair := newDigestPair(key)
	for i := range 256 {
		salt := [SalamanderSaltSize]byte{byte(i), byte(i ^ 0x5a)}
		hint := pair.deriveHint(salt[:])
		stream := pair.deriveStream(salt[:])
		// Reusing the scratch buffer must not mutate previously returned values.
		pair.deriveHint([]byte("nextsalt"))
		if hint != packetHint(key, salt[:]) || stream != packetStream(key, salt[:]) {
			t.Fatalf("cached digest mismatch for salt %x", salt)
		}
	}
}

func BenchmarkSalamanderXOR(b *testing.B) {
	for _, size := range []int{32, 64, 256, 1300, 9000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			src, dst := make([]byte, size), make([]byte, size)
			stream := [32]byte{1, 7, 42}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				xorWords(dst, src, &stream)
			}
		})
	}
}
