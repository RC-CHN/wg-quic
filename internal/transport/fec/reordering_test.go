package fec

import (
	"fmt"
	"testing"
	"time"
)

func TestDecoderReorderedGroupCompletionDoesNotInventLoss(t *testing.T) {
	decoder := NewDecoder()
	now := time.Unix(1, 0)
	// All source frames arrive, but QUIC packets carrying close frames are
	// briefly reordered behind more than one interleave window of groups.
	for id := range uint64(12) {
		result, err := decoder.Handle(now, marshalPacket(packet{
			kind: KindData, epoch: 1, groupID: id, payload: []byte{0, 1, byte(id)},
		}))
		if err != nil {
			t.Fatal(err)
		}
		for _, feedback := range result.SendFeedback {
			if feedback.Missing != 0 {
				t.Fatalf("reordering produced loss before the grace period: %+v", feedback)
			}
		}
	}
	for id := range uint64(12) {
		result, err := decoder.Handle(now.Add(time.Millisecond), marshalPacket(packet{
			kind: KindClose, epoch: 1, groupID: id, k: 1,
		}))
		if err != nil {
			t.Fatal(err)
		}
		for _, feedback := range result.SendFeedback {
			if feedback.Missing != 0 {
				t.Fatalf("reordered close produced invented loss: %+v", feedback)
			}
		}
	}
	feedbacks := decoder.Expire(now.Add(2 * completionGrace))
	if len(feedbacks) != 12 {
		t.Fatalf("got %d completed groups, want 12", len(feedbacks))
	}
	for _, feedback := range feedbacks {
		if feedback.Missing != 0 || feedback.Recovered != 0 {
			t.Fatalf("reordered group reported loss: %+v", feedback)
		}
	}
}

func TestDecoderReorderingGraceIsBounded(t *testing.T) {
	d := NewDecoder()
	now := time.Unix(1, 0)
	for id := range uint64(maxReceiveGroups) {
		if _, err := d.Handle(now, marshalPacket(packet{
			kind: KindData, epoch: 1, groupID: id, payload: []byte{0, 1, 42},
		})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Handle(now, marshalPacket(packet{
		kind: KindData, epoch: 1, groupID: maxReceiveGroups, payload: []byte{0, 1, 42},
	})); err == nil {
		t.Fatal("admitted new group beyond the receiver bound during grace")
	}
	// Closing an existing group must still work while the table is full.
	if _, err := d.Handle(now, marshalPacket(packet{kind: KindClose, epoch: 1, groupID: 0, k: 1})); err != nil {
		t.Fatal(err)
	}
	d.Expire(now.Add(completionGrace + time.Nanosecond))
	if len(d.groups) > MaxInterleave || len(d.groups) != len(d.groupExpiry) {
		t.Fatalf("stale groups not reclaimed: groups=%d expiry=%d", len(d.groups), len(d.groupExpiry))
	}
	d.Expire(now.Add(groupTTL + time.Nanosecond))
	if len(d.groups) != 0 || len(d.groupExpiry) != 0 || len(d.completed) != 0 {
		t.Fatal("receiver retained expired state")
	}
}

func BenchmarkDecoderReorderingGrace(b *testing.B) {
	for _, pending := range []int{1, 128, 1024} {
		b.Run(fmt.Sprint(pending), func(b *testing.B) {
			d := NewDecoder()
			now := time.Unix(1, 0)
			var raw []byte
			for id := range uint64(pending) {
				raw = marshalPacket(packet{kind: KindData, epoch: 1, groupID: id, payload: []byte{0, 1, 42}})
				if _, err := d.Handle(now, raw); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := d.Handle(now, raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
