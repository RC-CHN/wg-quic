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
	// MaxInterleave is the largest group-interleaving depth the encoder
	// supports. Data shards are emitted round-robin across this many in-flight
	// groups so a contiguous burst spreads over several groups instead of
	// wiping out one group wholesale.
	MaxInterleave = 4
	// QUIC loss counters provide the primary wake-up signal while protection is
	// bypassed. Keep an occasional end-to-end FEC probe as a safety net without
	// taxing high-rate healthy paths.
	healthyProbeFrames = 4096
)

type Encoder struct {
	epoch         uint16
	epochSnapshot atomic.Uint32
	nextGroupID   uint64
	maxData       int
	interleave    int
	controller    *Controller
	codecs        map[codecDimensions]reedsolomon.Encoder
	groups        []*encoderGroup
	current       int
	// bypass reuses the single-frame result slice returned on the healthy
	// fast path, where the encoder just forwards the frame without FEC work.
	// The encoder is session-local and Add is only called from one goroutine,
	// so the slice is safe to recycle between synchronous calls.
	bypass      [][]byte
	groupParity int
	lastParity  int
	unprotected int
}

type encoderGroup struct {
	groupID uint64
	epoch   uint16
	parity  int
	data    [][]byte
}

func NewEncoder(maxData int, controller *Controller) *Encoder {
	if maxData <= 0 || maxData > MaxDataShards {
		maxData = DefaultDataShards
	}
	if controller == nil {
		controller = NewController()
	}
	controller.SetDataShards(maxData)
	encoder := &Encoder{
		epoch: 1, maxData: maxData, controller: controller, interleave: 1,
		codecs: make(map[codecDimensions]reedsolomon.Encoder), lastParity: -1,
		bypass: make([][]byte, 1),
	}
	encoder.initGroups(1)
	encoder.epochSnapshot.Store(1)
	return encoder
}

// SetInterleave sets the number of concurrently emitted groups. A value of 1
// (the default) preserves sequential emission order. Increasing it flushes any
// in-flight groups before swapping the group set so a mid-stream change stays
// wire-consistent.
func (e *Encoder) SetInterleave(n int) error {
	if n < 1 {
		n = 1
	}
	if n > MaxInterleave {
		n = MaxInterleave
	}
	if n == e.interleave {
		return nil
	}
	if _, err := e.flushAll(); err != nil {
		return err
	}
	e.interleave = n
	e.initGroups(n)
	return nil
}

func (e *Encoder) initGroups(n int) {
	e.groups = make([]*encoderGroup, n)
	for i := range e.groups {
		e.groups[i] = &encoderGroup{}
	}
	e.current = 0
}

func (e *Encoder) Add(frame []byte) ([][]byte, error) {
	if len(frame) == 0 || len(frame) > MaxFrameSize {
		return nil, errors.New("invalid FEC data frame length")
	}
	targetParity := e.controller.CurrentParity()
	if targetParity == 0 && e.unprotected < healthyProbeFrames {
		e.unprotected++
		e.bypass[0] = frame
		return e.bypass, nil
	}
	var output [][]byte
	if e.lastParity >= 0 && targetParity != e.lastParity {
		flushed, err := e.flushAll()
		if err != nil {
			return nil, err
		}
		output = append(output, flushed...)
		e.epoch++
		if e.epoch == 0 {
			e.epoch = 1
		}
		e.epochSnapshot.Store(uint32(e.epoch))
	}
	e.lastParity = targetParity
	e.groupParity = e.controller.Parity(MaxDataShards)
	if e.groupParity == 0 {
		// Probe after a run of raw healthy-path frames. Most frames avoid
		// all FEC allocation, framing, close, and feedback work.
		e.groupParity = 1
		e.unprotected = 0
	}
	group := e.groups[e.current]
	if len(group.data) == 0 {
		group.groupID = e.nextGroupID
		e.nextGroupID++
		group.epoch = e.epoch
		group.parity = e.groupParity
	}
	shard := make([]byte, 2+len(frame))
	binary.BigEndian.PutUint16(shard[:2], uint16(len(frame)))
	copy(shard[2:], frame)
	index := len(group.data)
	group.data = append(group.data, shard)
	output = append(output, marshalPacket(packet{
		kind: KindData, epoch: group.epoch, groupID: group.groupID, index: uint16(index), payload: shard,
	}))
	if len(group.data) == e.maxData {
		closed, err := e.flushGroup(group)
		if err != nil {
			return nil, err
		}
		output = append(output, closed...)
	}
	e.current = (e.current + 1) % e.interleave
	return output, nil
}

func (e *Encoder) Pending() bool {
	for _, group := range e.groups {
		if len(group.data) != 0 {
			return true
		}
	}
	return false
}

func (e *Encoder) Flush() ([][]byte, error) {
	return e.flushAll()
}

func (e *Encoder) flushAll() ([][]byte, error) {
	var output [][]byte
	for _, group := range e.groups {
		flushed, err := e.flushGroup(group)
		if err != nil {
			return nil, err
		}
		output = append(output, flushed...)
	}
	return output, nil
}

func (e *Encoder) flushGroup(group *encoderGroup) ([][]byte, error) {
	k := len(group.data)
	if k == 0 {
		return nil, nil
	}
	r := min(group.parity, max(1, k/2))
	if r > MaxParityShards {
		r = MaxParityShards
	}
	output := make([][]byte, 0, r+1)
	if r > 0 {
		shardSize := 0
		for _, shard := range group.data {
			shardSize = max(shardSize, len(shard))
		}
		shards := make([][]byte, k+r)
		for i, shard := range group.data {
			// Encode only reads data shards, so a shard that already matches
			// the group size is passed through without a padding copy.
			shards[i] = padShard(shard, shardSize)
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
				kind: KindParity, epoch: group.epoch, groupID: group.groupID,
				index: uint16(i), k: uint16(k), r: uint16(r), payload: shards[k+i],
			}))
		}
	}
	output = append(output, marshalPacket(packet{
		kind: KindClose, epoch: group.epoch, groupID: group.groupID, k: uint16(k), r: uint16(r),
	}))
	group.data = group.data[:0]
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
