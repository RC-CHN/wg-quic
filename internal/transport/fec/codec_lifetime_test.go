package fec

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"runtime"
	"testing"
)

// Exercise the same cached codec for many different, recoverable erasure
// patterns. A connection can survive all of these without changing its FEC
// dimensions; retaining a decode matrix for every pattern must not grow its
// live heap for the connection's whole lifetime.
func TestCodecLifetimeVaryingLoss(t *testing.T) {
	cache := NewDecoder().codecs
	codec, err := cachedCodec(cache, MaxDataShards, MaxParityShards, 128)
	if err != nil {
		t.Fatal(err)
	}
	original := make([][]byte, MaxDataShards+MaxParityShards)
	for i := range original {
		original[i] = bytes.Repeat([]byte{byte(i + 1)}, 128)
	}
	if err := codec.Encode(original); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	var baseline, first, last runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&baseline)
	for round := range 20000 {
		shards := append([][]byte(nil), original...)
		for _, index := range rng.Perm(MaxDataShards)[:MaxParityShards] {
			shards[index] = nil
		}
		if err := codec.ReconstructData(shards); err != nil {
			t.Fatal(err)
		}
		for i := range MaxDataShards {
			if !bytes.Equal(shards[i], original[i]) {
				t.Fatalf("round %d reconstructed incorrect shard %d", round, i)
			}
		}
		if round == 9999 {
			runtime.GC()
			runtime.ReadMemStats(&first)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&last)
	runtime.KeepAlive(cache)
	t.Logf("20,000 recoveries: heap growth at 10k=%d, 20k=%d bytes; allocated=%d bytes",
		int64(first.HeapAlloc)-int64(baseline.HeapAlloc), int64(last.HeapAlloc)-int64(baseline.HeapAlloc), last.TotalAlloc-baseline.TotalAlloc)
	// The generous allowance covers runtime/pool noise, but not thousands of
	// persistent 32x32 inversion matrices (tens of megabytes before the fix).
	if growth := int64(last.HeapAlloc) - int64(baseline.HeapAlloc); growth > 8<<20 {
		t.Fatalf("decoder retains %d extra bytes after changing loss patterns", growth)
	}
}

func BenchmarkCachedCodecRecovery(b *testing.B) {
	for _, parity := range []int{1, 8} {
		for _, varying := range []bool{false, true} {
			b.Run(fmt.Sprintf("32+%d/varying=%t", parity, varying), func(b *testing.B) {
				codec, err := cachedCodec(NewDecoder().codecs, 32, parity, 1300)
				if err != nil {
					b.Fatal(err)
				}
				original := make([][]byte, 32+parity)
				for i := range original {
					original[i] = bytes.Repeat([]byte{byte(i + 1)}, 1300)
				}
				if err := codec.Encode(original); err != nil {
					b.Fatal(err)
				}
				rng := rand.New(rand.NewPCG(1, 2))
				patterns := make([][]int, 20000)
				for i := range patterns {
					patterns[i] = rng.Perm(32)[:parity]
				}
				b.ReportAllocs()
				b.SetBytes(32 * 1300)
				b.ResetTimer()
				for i := range b.N {
					shards := append([][]byte(nil), original...)
					pattern := patterns[0]
					if varying {
						pattern = patterns[i%len(patterns)]
					}
					for _, index := range pattern {
						shards[index] = nil
					}
					if err := codec.ReconstructData(shards); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
