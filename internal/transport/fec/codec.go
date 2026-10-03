package fec

import "github.com/klauspost/reedsolomon"

type codecDimensions struct {
	data   int
	parity int
}

// cachedCodec keeps matrix construction for the lifetime of one FEC direction.
// The library's inversion cache never evicts: use it only when the number of
// possible erasure patterns is small. Otherwise a long-lived lossy connection
// retains a new data-by-data matrix for almost every recovered group.
// Encoders and decoders are session-local, so codecs are never concurrent.
func cachedCodec(
	cache map[codecDimensions]reedsolomon.Encoder,
	data, parity, shardSize int,
) (reedsolomon.Encoder, error) {
	key := codecDimensions{data: data, parity: parity}
	if codec := cache[key]; codec != nil {
		return codec, nil
	}
	codec, err := reedsolomon.New(
		data,
		parity,
		reedsolomon.WithAutoGoroutines(shardSize),
		reedsolomon.WithInversionCache(boundedInversionPatterns(data, parity)),
	)
	if err != nil {
		return nil, err
	}
	if !boundedInversionPatterns(data, parity) {
		cached, err := reedsolomon.New(data, parity, reedsolomon.WithAutoGoroutines(shardSize))
		if err != nil {
			return nil, err
		}
		codec = &limitedInversionCodec{Encoder: codec, cached: cached, patterns: make(map[uint64]struct{})}
	}
	cache[key] = codec
	return codec, nil
}

// Keep common recurring loss patterns fast without teaching the underlying
// unbounded cache every new pattern seen over a connection's lifetime. Once
// full, unfamiliar patterns use the codec with inversion caching disabled.
type limitedInversionCodec struct {
	reedsolomon.Encoder
	cached   reedsolomon.Encoder
	patterns map[uint64]struct{}
}

func (c *limitedInversionCodec) ReconstructData(shards [][]byte) error {
	const maxCachedPatterns = 128
	var pattern uint64
	for i, shard := range shards {
		if len(shard) == 0 {
			pattern |= uint64(1) << uint(i)
		}
	}
	_, known := c.patterns[pattern]
	if known || len(c.patterns) < maxCachedPatterns {
		if err := c.cached.ReconstructData(shards); err != nil {
			return err
		}
		c.patterns[pattern] = struct{}{}
		return nil
	}
	return c.Encoder.ReconstructData(shards)
}

func boundedInversionPatterns(data, parity int) bool {
	const maxPatterns = 512
	// Sum C(data+parity, lost), an upper bound on the cache's keys. Keep
	// one-loss profiles fast (including the normal 32+1 profile), while
	// preventing the combinatorial growth of higher-protection profiles.
	patterns, combinations := 0, 1
	for lost := 1; lost <= parity; lost++ {
		combinations = combinations * (data + parity - lost + 1) / lost
		patterns += combinations
		if patterns > maxPatterns {
			return false
		}
	}
	return true
}
