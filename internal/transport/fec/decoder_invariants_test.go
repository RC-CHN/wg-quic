package fec

import (
	"testing"
	"time"
)

func reorderedFixture(t testing.TB) (*Decoder, []byte, time.Time) {
	t.Helper()
	now := time.Unix(1, 0)
	encoder := NewEncoder(2, NewController())
	decoder := NewDecoder()
	var late []byte
	for _, frame := range [][]byte{[]byte("first"), []byte("second")} {
		packets, err := encoder.Add(frame)
		if err != nil {
			t.Fatal(err)
		}
		for _, packet := range packets {
			p, _, err := parsePacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			if p.kind == KindData && p.index == 1 {
				late = packet
				continue
			}
			if _, err := decoder.Handle(now, packet); err != nil {
				t.Fatal(err)
			}
		}
	}
	if late == nil {
		t.Fatal("fixture did not emit a late shard")
	}
	return decoder, late, now
}

func TestLateReconstructedShardIsIdempotent(t *testing.T) {
	decoder, late, now := reorderedFixture(t)
	for range 5 {
		result, err := decoder.Handle(now, late)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Frames) != 0 {
			t.Fatal("reconstructed frame delivered again")
		}
	}
	feedback := decoder.Expire(now.Add(completionGrace + time.Millisecond))
	if len(feedback) != 1 || feedback[0].Missing != 0 || feedback[0].Recovered != 0 {
		t.Fatalf("duplicate late shards changed feedback: %+v", feedback)
	}
}

func TestInvalidLateShardCannotChangeAccounting(t *testing.T) {
	for _, kind := range []string{"epoch", "payload"} {
		t.Run(kind, func(t *testing.T) {
			decoder, late, now := reorderedFixture(t)
			p, _, _ := parsePacket(late)
			if kind == "epoch" {
				p.epoch++
			} else {
				p.payload = []byte{0, 99, 1}
			}
			_, err := decoder.Handle(now, marshalPacket(p))
			if kind == "epoch" && err == nil {
				t.Fatal("accepted a different epoch")
			}
			feedback := decoder.Expire(now.Add(completionGrace + time.Millisecond))
			if len(feedback) != 1 || feedback[0].Missing != 1 || feedback[0].Recovered != 1 {
				t.Fatalf("invalid shard changed feedback: %+v", feedback)
			}
		})
	}
}

func TestFeedbackCounterBounds(t *testing.T) {
	for _, feedback := range []Feedback{
		{Total: 2, Missing: 65535, Recovered: 65535},
		{Total: 2, Missing: 1, Recovered: 2},
		{Total: MaxDataShards + 1}, {Total: 0, Missing: 2},
	} {
		if _, err := NewDecoder().Handle(time.Now(), MarshalFeedback(feedback)); err == nil {
			t.Fatalf("accepted impossible counters: %+v", feedback)
		}
	}
	for _, feedback := range []Feedback{{}, {Missing: 1}, {Total: 2, Missing: 1, Recovered: 1}} {
		if _, err := NewDecoder().Handle(time.Now(), MarshalFeedback(feedback)); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzLateShardAccounting(f *testing.F) {
	f.Add(uint8(2), uint8(0), false)
	f.Add(uint8(0), uint8(5), true)
	f.Fuzz(func(t *testing.T, duplicates, delay uint8, badEpoch bool) {
		decoder, late, now := reorderedFixture(t)
		if badEpoch {
			late = append([]byte(nil), late...)
			late[7]++
		}
		check := func(feedbacks []Feedback) {
			for _, feedback := range feedbacks {
				if !validFeedback(feedback.Total, feedback.Missing, feedback.Recovered) {
					t.Fatalf("invalid decoder accounting: %+v", feedback)
				}
			}
		}
		for i := 0; i < int(duplicates)%16; i++ {
			result, _ := decoder.Handle(now.Add(time.Duration(delay)*time.Millisecond), late)
			check(result.SendFeedback)
		}
		check(decoder.Expire(now.Add(groupTTL + time.Millisecond)))
	})
}
