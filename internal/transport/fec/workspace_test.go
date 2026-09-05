package fec

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestEncoderWorkspacePreservesPacketsAndClearsPadding(t *testing.T) {
	controller := NewController()
	controller.setParity(2)
	encoder := NewEncoder(8, controller)
	var retained, snapshots [][]byte
	for round := range 16 {
		var packets [][]byte
		var frames [][]byte
		for i := range 8 {
			// Alternate long and short groups to exercise growth, shrinking,
			// and zero-padding of buffers previously filled with other bytes.
			sizes := []int{1, 31, 1000, 1300, 2048, MaxFrameSize}
			frame := bytes.Repeat([]byte{byte(round*8 + i + 1)}, sizes[(round+i)%len(sizes)])
			frames = append(frames, frame)
			out, err := encoder.Add(frame)
			if err != nil {
				t.Fatal(err)
			}
			packets = append(packets, out...)
		}
		for i, packet := range retained {
			if !bytes.Equal(packet, snapshots[i]) {
				t.Fatalf("round %d overwrote retained packet %d", round, i)
			}
		}
		decoder := NewDecoder()
		now := time.Now()
		var recovered [][]byte
		for _, raw := range packets {
			p, _, err := parsePacket(raw)
			if err != nil {
				t.Fatal(err)
			}
			if p.kind == KindData && (p.index == 2 || p.index == 5) {
				continue
			}
			result, err := decoder.Handle(now, raw)
			if err != nil {
				t.Fatal(err)
			}
			recovered = append(recovered, result.Frames...)
		}
		if len(recovered) != len(frames) {
			t.Fatalf("round %d recovered %d frames, want %d", round, len(recovered), len(frames))
		}
		for _, want := range frames {
			found := false
			for _, got := range recovered {
				found = found || bytes.Equal(got, want)
			}
			if !found {
				t.Fatalf("round %d lost or corrupted frame of length %d", round, len(want))
			}
		}
		for _, raw := range packets {
			retained = append(retained, raw)
			snapshots = append(snapshots, bytes.Clone(raw))
		}
	}
}

func TestDecoderExpirySchedule(t *testing.T) {
	now := time.Unix(1, 0)
	decoder := NewDecoder()
	// An incomplete group first schedules the much later group TTL.
	if _, err := decoder.Handle(now, marshalPacket(packet{
		kind: KindClose, epoch: 1, groupID: 0, k: 2, r: 1,
	})); err != nil {
		t.Fatal(err)
	}
	// A later completed group must pull that deadline forward to grace.
	for _, p := range []packet{
		{kind: KindData, epoch: 1, groupID: 1, payload: []byte{0, 1, 42}},
		{kind: KindClose, epoch: 1, groupID: 1, k: 1},
	} {
		if _, err := decoder.Handle(now, marshalPacket(p)); err != nil {
			t.Fatal(err)
		}
	}
	if got := decoder.Expire(now.Add(completionGrace)); len(got) != 0 {
		t.Fatalf("expired at exact grace boundary: %+v", got)
	}
	got := decoder.Expire(now.Add(completionGrace + time.Nanosecond))
	if len(got) != 1 || got[0].GroupID != 1 || got[0].Missing != 0 {
		t.Fatalf("completed group feedback = %+v", got)
	}
	if got := decoder.Expire(now.Add(groupTTL)); len(got) != 0 {
		t.Fatalf("expired at exact TTL boundary: %+v", got)
	}
	got = decoder.Expire(now.Add(groupTTL + time.Nanosecond))
	if len(got) != 1 || got[0].GroupID != 0 || got[0].Missing != 2 {
		t.Fatalf("incomplete group feedback = %+v", got)
	}
	// The empty decoder must wake up again for a newly arriving group.
	now = now.Add(2 * groupTTL)
	if _, err := decoder.Handle(now, marshalPacket(packet{
		kind: KindClose, epoch: 1, groupID: 2, k: 1, r: 1,
	})); err != nil {
		t.Fatal(err)
	}
	if got := decoder.Expire(now.Add(groupTTL + time.Nanosecond)); len(got) != 1 {
		t.Fatalf("new group was not expired: %+v", got)
	}
}

func BenchmarkDecoderRetainedGroups(b *testing.B) {
	for _, count := range []int{1, 128, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			d := NewDecoder()
			now := time.Unix(1, 0)
			for i := range count {
				for _, p := range []packet{
					{kind: KindData, epoch: 1, groupID: uint64(i), payload: []byte{0, 1, 42}},
					{kind: KindClose, epoch: 1, groupID: uint64(i), k: 1},
				} {
					if _, err := d.Handle(now, marshalPacket(p)); err != nil {
						b.Fatal(err)
					}
				}
			}
			// Late duplicates during reordering grace must be cheap even
			// with many completed groups; the wire input is unchanged.
			raw := marshalPacket(packet{kind: KindData, epoch: 1, groupID: uint64(count - 1), payload: []byte{0, 1, 42}})
			b.ReportAllocs()
			for b.Loop() {
				if _, err := d.Handle(now, raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
