package fec

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/klauspost/reedsolomon"
)

const (
	groupTTL           = 3 * time.Second
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
	created   time.Time
	epoch     uint16
	groupID   uint64
	k         int
	r         int
	data      map[int][]byte
	parity    map[int][]byte
	delivered map[int]bool
	closed    bool
}

type Decoder struct {
	groups    map[uint64]*receiveGroup
	completed map[uint64]time.Time
}

func NewDecoder() *Decoder {
	return &Decoder{
		groups:    make(map[uint64]*receiveGroup),
		completed: make(map[uint64]time.Time),
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
	if _, ok := d.completed[p.groupID]; ok {
		return result, nil
	}
	group := d.groups[p.groupID]
	if group == nil {
		if len(d.groups) >= maxReceiveGroups {
			return result, errors.New("too many incomplete FEC groups")
		}
		group = &receiveGroup{
			created: now, epoch: p.epoch, groupID: p.groupID,
			data: make(map[int][]byte), parity: make(map[int][]byte), delivered: make(map[int]bool),
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
		if !group.delivered[index] {
			frame, err := decodeDataShard(p.payload)
			if err != nil {
				return result, err
			}
			group.delivered[index] = true
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
	if feedback != nil {
		result.SendFeedback = append(result.SendFeedback, *feedback)
	}
	if final {
		delete(d.groups, group.groupID)
		d.limitCompleted()
		d.completed[group.groupID] = now
	}
	return result, nil
}

// Expire advances receiver state even when no more datagrams arrive.
func (d *Decoder) Expire(now time.Time) []Feedback {
	return d.expire(now)
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
			shards[i] = make([]byte, shardSize)
			copy(shards[i], shard)
		}
	}
	for i := 0; i < group.r; i++ {
		if shard := group.parity[i]; shard != nil {
			shards[group.k+i] = make([]byte, shardSize)
			copy(shards[group.k+i], shard)
		}
	}
	codec, err := reedsolomon.New(group.k, group.r, reedsolomon.WithAutoGoroutines(shardSize))
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
		if !group.delivered[i] {
			group.delivered[i] = true
			frames = append(frames, frame)
		}
	}
	return frames, feedback(len(frames)), true, nil
}

func decodeDataShard(shard []byte) ([]byte, error) {
	if len(shard) < 2 {
		return nil, errors.New("reconstructed FEC shard is too short")
	}
	length := int(binary.BigEndian.Uint16(shard[:2]))
	if length == 0 || length > len(shard)-2 {
		return nil, errors.New("invalid reconstructed FEC frame length")
	}
	return append([]byte(nil), shard[2:2+length]...), nil
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
	for id, completedAt := range d.completed {
		if now.Sub(completedAt) > groupTTL {
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
	for id, completedAt := range d.completed {
		if oldestAt.IsZero() || completedAt.Before(oldestAt) {
			oldestID, oldestAt = id, completedAt
		}
	}
	delete(d.completed, oldestID)
}
