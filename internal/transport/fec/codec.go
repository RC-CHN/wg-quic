package fec

import "github.com/klauspost/reedsolomon"

type codecDimensions struct {
	data   int
	parity int
}

// cachedCodec keeps the expensive matrix construction and inversion cache for
// the lifetime of one FEC direction. Encoders and decoders are session-local,
// so the returned codec is never used concurrently.
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
	)
	if err != nil {
		return nil, err
	}
	cache[key] = codec
	return codec, nil
}
