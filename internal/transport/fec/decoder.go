package fec

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/klauspost/reedsolomon"
)

const (
	groupTTL           = 3 * time.Second
	completionGrace    = 10 * time.Millisecond
	maxReceiveGroups   = 1024
	maxCompletedGroups = 4096
)

type Result struct {
	Handled          bool
	Frames           [][]byte
	SendFeedback     []Feedback
	ObservedFeedback *Feedback
}

type receiveGroup struct {
	created       time.Time
	epoch         uint16
	groupID       uint64
	k             int
	r             int
	data          map[int][]byte
	parity        map[int][]byte
	delivered     uint64 // bit i is set once shard i has been delivered
	reconstructed uint64 // bit i is set once shard i was reconstructed from parity
	closed        bool
}

// completedGroup retains the small amount of state needed to absorb late
// shards after a group is finalized, without keeping the full shard payloads.
// Feedback is deferred until the grace window expires so reordering is not
// reported as loss.
type completedGroup struct {
	finalized     time.Time
	epoch         uint16
	k             int
	delivered     uint64
	reconstructed uint64
	missing       uint16
	recovered     uint16
}

type Decoder struct {
	groups      map[uint64]*receiveGroup
	completed   map[uint64]*completedGroup
	codecs      map[codecDimensions]reedsolomon.Encoder
	lastGroupID uint64
}

func NewDecoder() *Decoder {
	return &Decoder{
		groups:    make(map[uint64]*receiveGroup),
		completed: make(map[uint64]*completedGroup),
		codecs:    make(map[codecDimensions]reedsolomon.Encoder),
	}
}

func (d *Decoder) Handle(now time.Time, data []byte) (Result, error) {
	p, handled, err := parsePacket(data)
	if err != nil || !handled {
		return Result{Handled: handled}, err
	}
	result := Result{Handled: true}
	if p.kind == KindFeedback {
		result.ObservedFeedback = &Feedback{
			Epoch: p.epoch, GroupID: p.groupID, Missing: p.index, Total: p.k, Recovered: p.r,
		}
		return result, nil
	}
	result.SendFeedback = append(result.SendFeedback, d.expire(now)...)
	if p.groupID > d.lastGroupID {
		d.lastGroupID = p.groupID
	}
	result.SendFeedback = append(result.SendFeedback, d.fastExpire()...)
	if done := d.completed[p.groupID]; done != nil {
		d.handleLateShard(done, p, &result)
		return result, nil
	}
	group := d.groups[p.groupID]
	if group == nil {
		if len(d.groups) >= maxReceiveGroups {
			return result, errors.New("too many incomplete FEC groups")
		}
		group = &receiveGroup{
			created: now, epoch: p.epoch, groupID: p.groupID,
			data: make(map[int][]byte), parity: make(map[int][]byte),
		}
		d.groups[p.groupID] = group
	}
	if group.epoch != p.epoch {
		return result, errors.New("FEC epoch changed within group")
	}
	switch p.kind {
	case KindData:
		if int(p.index) >= MaxDataShards || len(p.payload) < 3 {
			return result, errors.New("invalid FEC data shard")
		}
		index := int(p.index)
		if group.data[index] == nil {
			group.data[index] = append([]byte(nil), p.payload...)
		}
		bit := uint64(1) << uint(index)
		if group.delivered&bit == 0 {
			// Decode from the stored copy: the delivered frame may outlive the
			// pooled receive datagram that p.payload aliases.
			frame, err := decodeDataShard(group.data[index])
			if err != nil {
				return result, err
			}
			group.delivered |= bit
			result.Frames = append(result.Frames, frame)
		}
	case KindParity:
		if err := group.setDimensions(int(p.k), int(p.r)); err != nil {
			return result, err
		}
		if int(p.index) >= group.r || len(p.payload) == 0 {
			return result, errors.New("invalid FEC parity shard")
		}
		if group.parity[int(p.index)] == nil {
			group.parity[int(p.index)] = append([]byte(nil), p.payload...)
		}
	case KindClose:
		if err := group.setDimensions(int(p.k), int(p.r)); err != nil {
			return result, err
		}
		group.closed = true
	}
	recovered, feedback, final, err := d.tryComplete(group)
	if err != nil {
		return result, err
	}
	result.Frames = append(result.Frames, recovered...)
	if final {
		delete(d.groups, group.groupID)
		d.limitCompleted()
		d.completed[group.groupID] = &completedGroup{
			finalized:     now,
			epoch:         group.epoch,
			k:             group.k,
			delivered:     group.delivered,
			reconstructed: group.reconstructed,
			missing:       feedback.Missing,
			recovered:     feedback.Recovered,
		}
	}
	return result, nil
}

// handleLateShard absorbs a shard arriving after its group was finalized. A
// shard that was already reconstructed was only reordered (false loss), so its
// missing/recovered counts are written back. A shard that was never delivered
// is a genuine late arrival and is delivered now. Anything else is a duplicate.
func (d *Decoder) handleLateShard(done *completedGroup, p packet, result *Result) {
	if p.kind != KindData || int(p.index) >= done.k || len(p.payload) < 3 {
		return
	}
	bit := uint64(1) << uint(p.index)
	switch {
	case done.reconstructed&bit != 0:
		// Reconstructed at finalization: the shard was reordered, not lost.
		done.missing--
		done.recovered--
	case done.delivered&bit == 0:
		// Never delivered. Deliver a copy now; the pooled receive datagram
		// backing p.payload is released after Handle returns.
		frame, err := decodeDataShard(p.payload)
		if err != nil {
			return
		}
		done.delivered |= bit
		done.missing--
		result.Frames = append(result.Frames, append([]byte(nil), frame...))
	default:
		// Already delivered from the original shard: pure duplicate.
	}
}

// Expire advances receiver state even when no more datagrams arrive.
func (d *Decoder) Expire(now time.Time) []Feedback {
	return d.expire(now)
}

// fastExpire reclaims incomplete groups left behind once a newer group
// arrives. DATAGRAM frames are not reordered, so when a shard with group ID G
// shows up, every older incomplete group must have lost its close frame (or
// enough shards) and can be reported immediately instead of waiting out
// groupTTL. This keeps the controller's parity response fast under burst loss.
func (d *Decoder) fastExpire() []Feedback {
	var feedback []Feedback
	for id, group := range d.groups {
		// DATAGRAM frames are not reordered, but interleaving emits up to
		// MaxInterleave groups concurrently, so an older group may still be in
		// flight. Only groups lagging the newest by at least MaxInterleave are
		// certainly lost.
		if d.lastGroupID-id < MaxInterleave {
			continue
		}
		if group.k > 0 {
			received := 0
			for i := 0; i < group.k; i++ {
				if group.data[i] != nil {
					received++
				}
			}
			feedback = append(feedback, Feedback{
				Epoch: group.epoch, GroupID: group.groupID,
				Missing: uint16(group.k - received), Total: uint16(group.k),
			})
		} else {
			// The whole group vanished before its dimensions arrived.
			// Report one unrecovered shard so the controller still reacts.
			feedback = append(feedback, Feedback{
				Epoch: group.epoch, GroupID: group.groupID,
				Missing: 1, Recovered: 0, Total: 0,
			})
		}
		delete(d.groups, id)
	}
	return feedback
}

func (g *receiveGroup) setDimensions(k, r int) error {
	if k <= 0 || k > MaxDataShards || r < 0 || r > MaxParityShards {
		return errors.New("invalid FEC group dimensions")
	}
	if g.k != 0 && (g.k != k || g.r != r) {
		return errors.New("inconsistent FEC group dimensions")
	}
	g.k, g.r = k, r
	return nil
}

func (d *Decoder) tryComplete(group *receiveGroup) ([][]byte, *Feedback, bool, error) {
	if group.k == 0 {
		return nil, nil, false, nil
	}
	original := 0
	available := 0
	for i := 0; i < group.k; i++ {
		if group.data[i] != nil {
			original++
			available++
		}
	}
	for i := 0; i < group.r; i++ {
		if group.parity[i] != nil {
			available++
		}
	}
	missing := group.k - original
	feedback := func(recovered int) *Feedback {
		return &Feedback{
			Epoch: group.epoch, GroupID: group.groupID, Missing: uint16(missing),
			Recovered: uint16(recovered), Total: uint16(group.k),
		}
	}
	if missing == 0 {
		if group.closed || group.r == 0 {
			return nil, feedback(0), true, nil
		}
		return nil, nil, false, nil
	}
	if available < group.k {
		if group.closed && group.r == 0 {
			return nil, feedback(0), true, nil
		}
		return nil, nil, false, nil
	}
	shardSize := 0
	for _, shard := range group.data {
		shardSize = max(shardSize, len(shard))
	}
	for _, shard := range group.parity {
		shardSize = max(shardSize, len(shard))
	}
	shards := make([][]byte, group.k+group.r)
	for i := 0; i < group.k; i++ {
		if shard := group.data[i]; shard != nil {
			// ReconstructData writes only missing slots, so present shards
			// that already match the group size are passed through.
			shards[i] = padShard(shard, shardSize)
		}
	}
	for i := 0; i < group.r; i++ {
		if shard := group.parity[i]; shard != nil {
			shards[group.k+i] = padShard(shard, shardSize)
		}
	}
	codec, err := cachedCodec(d.codecs, group.k, group.r, shardSize)
	if err != nil {
		return nil, nil, false, err
	}
	if err := codec.ReconstructData(shards); err != nil {
		if group.closed {
			return nil, feedback(0), true, nil
		}
		return nil, nil, false, nil
	}
	frames := make([][]byte, 0, missing)
	for i := 0; i < group.k; i++ {
		if group.data[i] != nil {
			continue
		}
		frame, err := decodeDataShard(shards[i])
		if err != nil {
			return nil, nil, false, err
		}
		group.data[i] = shards[i]
		group.reconstructed |= uint64(1) << uint(i)
		bit := uint64(1) << uint(i)
		if group.delivered&bit == 0 {
			group.delivered |= bit
			frames = append(frames, frame)
		}
	}
	return frames, feedback(len(frames)), true, nil
}

// padShard returns shard ready for the Reed-Solomon codec: the slice itself
// when it already matches shardSize, otherwise a zero-padded copy. The codecs
// never mutate present shards, only missing slots.
func padShard(shard []byte, shardSize int) []byte {
	if len(shard) == shardSize {
		return shard
	}
	padded := make([]byte, shardSize)
	copy(padded, shard)
	return padded
}

// decodeDataShard returns the framed payload as a sub-slice of shard. The
// result stays valid as long as shard's owner retains it; callers must not
// mutate it.
func decodeDataShard(shard []byte) ([]byte, error) {
	if len(shard) < 2 {
		return nil, errors.New("reconstructed FEC shard is too short")
	}
	length := int(binary.BigEndian.Uint16(shard[:2]))
	if length == 0 || length > len(shard)-2 {
		return nil, errors.New("invalid reconstructed FEC frame length")
	}
	return shard[2 : 2+length], nil
}

func (d *Decoder) expire(now time.Time) []Feedback {
	var feedback []Feedback
	for id, group := range d.groups {
		if now.Sub(group.created) > groupTTL {
			if group.k > 0 {
				received := 0
				for i := 0; i < group.k; i++ {
					if group.data[i] != nil {
						received++
					}
				}
				feedback = append(feedback, Feedback{
					Epoch: group.epoch, GroupID: group.groupID,
					Missing: uint16(group.k - received), Total: uint16(group.k),
				})
			}
			delete(d.groups, id)
		}
	}
	for id, done := range d.completed {
		if now.Sub(done.finalized) > groupTTL {
			delete(d.completed, id)
			continue
		}
		if now.Sub(done.finalized) > completionGrace {
			feedback = append(feedback, Feedback{
				Epoch: done.epoch, GroupID: id,
				Missing: done.missing, Recovered: done.recovered, Total: uint16(done.k),
			})
			delete(d.completed, id)
		}
	}
	return feedback
}

func (d *Decoder) limitCompleted() {
	if len(d.completed) < maxCompletedGroups {
		return
	}
	var oldestID uint64
	var oldestAt time.Time
	for id, done := range d.completed {
		if oldestAt.IsZero() || done.finalized.Before(oldestAt) {
			oldestID, oldestAt = id, done.finalized
		}
	}
	delete(d.completed, oldestID)
}
