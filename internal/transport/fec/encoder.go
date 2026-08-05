package fec

import (
	"encoding/binary"
	"errors"
	"sync/atomic"
	"time"

	"github.com/klauspost/reedsolomon"
)

const (
	DefaultDataShards = 8
	MaxDataShards     = 32
	MaxParityShards   = 8
	MaxFrameSize      = 4096
	// QUIC loss counters provide the primary wake-up signal while protection is
	// bypassed. Keep an occasional end-to-end FEC probe as a safety net without
	// taxing high-rate healthy paths.
	healthyProbeFrames = 4096
)

type Encoder struct {
	epoch         uint16
	epochSnapshot atomic.Uint32
	groupID       uint64
	maxData       int
	controller    *Controller
	codecs        map[codecDimensions]reedsolomon.Encoder
	data          [][]byte
	groupParity   int
	lastParity    int
	unprotected   int
}

func NewEncoder(maxData int, controller *Controller) *Encoder {
	if maxData <= 0 || maxData > MaxDataShards {
		maxData = DefaultDataShards
	}
	if controller == nil {
		controller = NewController()
	}
	encoder := &Encoder{
		epoch: 1, maxData: maxData, controller: controller,
		codecs: make(map[codecDimensions]reedsolomon.Encoder), lastParity: -1,
	}
	encoder.epochSnapshot.Store(1)
	return encoder
}

func (e *Encoder) Add(frame []byte) ([][]byte, error) {
	if len(frame) == 0 || len(frame) > MaxFrameSize {
		return nil, errors.New("invalid FEC data frame length")
	}
	if len(e.data) == 0 {
		targetParity := e.controller.CurrentParity()
		if targetParity == 0 && e.unprotected < healthyProbeFrames {
			e.unprotected++
			return [][]byte{frame}, nil
		}
		if e.lastParity >= 0 && targetParity != e.lastParity {
			e.epoch++
			if e.epoch == 0 {
				e.epoch = 1
			}
			e.epochSnapshot.Store(uint32(e.epoch))
		}
		e.lastParity = targetParity
		e.groupID++
		e.groupParity = e.controller.Parity(MaxDataShards)
		if e.groupParity == 0 {
			// Probe after a run of raw healthy-path frames. Most frames avoid
			// all FEC allocation, framing, close, and feedback work.
			e.groupParity = 1
			e.unprotected = 0
		}
	}
	shard := make([]byte, 2+len(frame))
	binary.BigEndian.PutUint16(shard[:2], uint16(len(frame)))
	copy(shard[2:], frame)
	index := len(e.data)
	e.data = append(e.data, shard)
	output := [][]byte{marshalPacket(packet{
		kind: KindData, epoch: e.epoch, groupID: e.groupID, index: uint16(index), payload: shard,
	})}
	if len(e.data) == e.maxData {
		closed, err := e.Flush()
		if err != nil {
			return nil, err
		}
		output = append(output, closed...)
	}
	return output, nil
}

func (e *Encoder) Pending() bool {
	return len(e.data) != 0
}

func (e *Encoder) Flush() ([][]byte, error) {
	k := len(e.data)
	if k == 0 {
		return nil, nil
	}
	r := min(e.groupParity, max(1, k/2))
	if r > MaxParityShards {
		r = MaxParityShards
	}
	output := make([][]byte, 0, r+1)
	if r > 0 {
		shardSize := 0
		for _, shard := range e.data {
			shardSize = max(shardSize, len(shard))
		}
		shards := make([][]byte, k+r)
		for i, shard := range e.data {
			shards[i] = make([]byte, shardSize)
			copy(shards[i], shard)
		}
		for i := k; i < k+r; i++ {
			shards[i] = make([]byte, shardSize)
		}
		codec, err := cachedCodec(e.codecs, k, r, shardSize)
		if err != nil {
			return nil, err
		}
		if err := codec.Encode(shards); err != nil {
			return nil, err
		}
		for i := 0; i < r; i++ {
			output = append(output, marshalPacket(packet{
				kind: KindParity, epoch: e.epoch, groupID: e.groupID,
				index: uint16(i), k: uint16(k), r: uint16(r), payload: shards[k+i],
			}))
		}
	}
	output = append(output, marshalPacket(packet{
		kind: KindClose, epoch: e.epoch, groupID: e.groupID, k: uint16(k), r: uint16(r),
	}))
	e.data = e.data[:0]
	return output, nil
}

func (e *Encoder) Observe(feedback Feedback) {
	if uint32(feedback.Epoch) == e.epochSnapshot.Load() {
		e.controller.Observe(feedback)
	}
}

func (e *Encoder) ObserveTransport(packetsSent, packetsLost uint64) {
	e.controller.ObserveTransport(packetsSent, packetsLost)
}

func (e *Encoder) ObservePathRTT(rtt time.Duration) {
	e.controller.ObservePathRTT(rtt)
}

func (e *Encoder) Stats() (parity int, lossEstimatePPM uint64) {
	return e.controller.CurrentParity(), e.controller.LossEstimatePPM()
}
