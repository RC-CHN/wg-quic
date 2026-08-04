package fec

import (
	"bytes"
	"testing"
	"time"
)

func TestSystematicDeliveryAndRecovery(t *testing.T) {
	controller := NewController()
	encoder := NewEncoder(4, controller)
	var packets [][]byte
	frames := [][]byte{
		bytes.Repeat([]byte{1}, 31),
		bytes.Repeat([]byte{2}, 800),
		bytes.Repeat([]byte{3}, 117),
		bytes.Repeat([]byte{4}, 1000),
	}
	for _, frame := range frames {
		output, err := encoder.Add(frame)
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, output...)
	}

	decoder := NewDecoder()
	var got [][]byte
	var feedback *Feedback
	for i, packet := range packets {
		p, handled, err := parsePacket(packet)
		if err != nil || !handled {
			t.Fatalf("parse packet: handled=%v err=%v", handled, err)
		}
		if p.kind == KindData && p.index == 1 {
			continue
		}
		result, err := decoder.Handle(time.Now(), packet)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		got = append(got, result.Frames...)
		if len(result.SendFeedback) != 0 {
			feedback = &result.SendFeedback[len(result.SendFeedback)-1]
		}
	}
	if feedback == nil || feedback.Missing != 1 || feedback.Recovered != 1 {
		t.Fatalf("feedback = %#v, want one recovered shard", feedback)
	}
	if len(got) != len(frames) {
		t.Fatalf("delivered %d frames, want %d", len(got), len(frames))
	}
	for _, want := range frames {
		found := false
		for _, frame := range got {
			if bytes.Equal(frame, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("frame of length %d was not delivered", len(want))
		}
	}
}

func TestControllerIncreasesFastAndDecreasesSlowly(t *testing.T) {
	controller := NewController()
	if got := controller.Parity(8); got != 1 {
		t.Fatalf("initial parity = %d, want 1", got)
	}
	controller.Observe(Feedback{Total: 8, Missing: 2})
	if got := controller.Parity(8); got != 2 {
		t.Fatalf("parity after unrecovered loss = %d, want 2", got)
	}
	for i := 0; i < 31; i++ {
		controller.Observe(Feedback{Total: 8})
	}
	if got := controller.Parity(8); got != 2 {
		t.Fatalf("parity decreased before hysteresis window: %d", got)
	}
	controller.Observe(Feedback{Total: 8})
	if got := controller.Parity(8); got != 1 {
		t.Fatalf("parity after zero-loss window = %d, want 1", got)
	}
}

func TestControllerLeavesHealthyFastPathOnRecoveredProbeLoss(t *testing.T) {
	controller := NewController()
	controller.setParity(0)
	if !controller.Observe(Feedback{Total: 8, Missing: 1, Recovered: 1}) {
		t.Fatal("recovered probe loss did not change the controller")
	}
	if got := controller.Parity(8); got != 1 {
		t.Fatalf("parity after recovered probe loss = %d, want 1", got)
	}
}

func TestControllerTargetsMinimumUsefulParity(t *testing.T) {
	tests := []struct {
		loss float64
		want int
	}{
		{loss: 0, want: 0},
		{loss: 0.005, want: 1},
		{loss: 0.02, want: 2},
		{loss: 0.05, want: 3},
		{loss: 0.10, want: 4},
	}
	for _, test := range tests {
		if got := parityForLoss(DefaultDataShards, test.loss); got != test.want {
			t.Errorf("parityForLoss(8, %.3f) = %d, want %d", test.loss, got, test.want)
		}
	}
}

func TestControllerUsesTransportLossWhileFECIsBypassed(t *testing.T) {
	controller := NewController()
	controller.setParity(0)
	if controller.ObserveTransport(100, 0) {
		t.Fatal("initial transport snapshot changed parity")
	}
	if !controller.ObserveTransport(228, 8) {
		t.Fatal("transport loss did not leave the zero-parity fast path")
	}
	if got := controller.CurrentParity(); got != 1 {
		t.Fatalf("parity after transport loss = %d, want 1", got)
	}
	if got := controller.LossEstimatePPM(); got == 0 {
		t.Fatal("transport loss did not update the loss estimate")
	}
}

func TestDecoderBoundsIncompleteAndCompletedGroups(t *testing.T) {
	decoder := NewDecoder()
	now := time.Now()
	for i := 0; i < maxReceiveGroups; i++ {
		packet := marshalPacket(packet{
			kind: KindData, epoch: 1, groupID: uint64(i + 1), payload: []byte{0, 1, byte(i)},
		})
		if _, err := decoder.Handle(now, packet); err != nil {
			t.Fatalf("group %d: %v", i, err)
		}
	}
	excess := marshalPacket(packet{
		kind: KindData, epoch: 1, groupID: maxReceiveGroups + 1, payload: []byte{0, 1, 1},
	})
	if _, err := decoder.Handle(now, excess); err == nil {
		t.Fatal("decoder accepted more than the incomplete-group limit")
	}

	decoder = NewDecoder()
	for i := 0; i < maxCompletedGroups+20; i++ {
		groupID := uint64(i + 1)
		data := marshalPacket(packet{
			kind: KindData, epoch: 1, groupID: groupID, index: 0, payload: []byte{0, 1, byte(i)},
		})
		closePacket := marshalPacket(packet{
			kind: KindClose, epoch: 1, groupID: groupID, k: 1,
		})
		if _, err := decoder.Handle(now, data); err != nil {
			t.Fatal(err)
		}
		if _, err := decoder.Handle(now, closePacket); err != nil {
			t.Fatal(err)
		}
	}
	if len(decoder.completed) > maxCompletedGroups {
		t.Fatalf("completed cache grew to %d entries", len(decoder.completed))
	}
}

func TestDecoderExpiresWithoutAnotherPacket(t *testing.T) {
	decoder := NewDecoder()
	now := time.Now()
	closePacket := marshalPacket(packet{
		kind: KindClose, epoch: 1, groupID: 9, k: 2, r: 1,
	})
	if _, err := decoder.Handle(now, closePacket); err != nil {
		t.Fatal(err)
	}
	feedback := decoder.Expire(now.Add(groupTTL + time.Millisecond))
	if len(feedback) != 1 || feedback[0].Missing != 2 || feedback[0].Total != 2 {
		t.Fatalf("expiration feedback = %#v", feedback)
	}
}

func TestEncoderMovesToNewEpochWhenParityChanges(t *testing.T) {
	encoder := NewEncoder(2, NewController())
	first, err := encoder.Add([]byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := parsePacket(first[0])
	if err != nil {
		t.Fatal(err)
	}
	encoder.Observe(Feedback{Epoch: parsed.epoch, Total: 2, Missing: 2})
	second, err := encoder.Add([]byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	parsedSecond, _, err := parsePacket(second[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsedSecond.epoch != parsed.epoch {
		t.Fatal("encoder changed epoch in the middle of a group")
	}
	third, err := encoder.Add([]byte("third"))
	if err != nil {
		t.Fatal(err)
	}
	parsedThird, _, err := parsePacket(third[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsedThird.epoch == parsed.epoch {
		t.Fatal("encoder kept the old epoch after changing parity")
	}
}

func TestEncoderBypassesFECAndPeriodicallyProbesOnHealthyPath(t *testing.T) {
	controller := NewController()
	controller.setParity(0)
	encoder := NewEncoder(DefaultDataShards, controller)
	frame := []byte("healthy")
	for i := 0; i < healthyProbeFrames; i++ {
		packets, err := encoder.Add(frame)
		if err != nil {
			t.Fatal(err)
		}
		if len(packets) != 1 || !bytes.Equal(packets[0], frame) {
			t.Fatalf("healthy frame %d was FEC framed: %#v", i, packets)
		}
	}
	packets, err := encoder.Add(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 1 {
		t.Fatalf("probe first shard produced %d packets, want 1", len(packets))
	}
	if kind, ok := PacketKind(packets[0]); !ok || kind != KindData {
		t.Fatalf("probe packet kind = %v, handled=%t", kind, ok)
	}
}

func BenchmarkEncoderGroup(b *testing.B) {
	for _, parity := range []int{0, 1, 2, 4} {
		b.Run(string(rune('0'+parity))+"-parity", func(b *testing.B) {
			controller := NewController()
			controller.setParity(parity)
			encoder := NewEncoder(DefaultDataShards, controller)
			frame := bytes.Repeat([]byte{1}, 1000)
			b.SetBytes(DefaultDataShards * int64(len(frame)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for range DefaultDataShards {
					if _, err := encoder.Add(frame); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func BenchmarkDecoderRecovery(b *testing.B) {
	controller := NewController()
	controller.setParity(2)
	encoder := NewEncoder(DefaultDataShards, controller)
	frame := bytes.Repeat([]byte{1}, 1000)
	var encoded [][]byte
	for range DefaultDataShards {
		packets, err := encoder.Add(frame)
		if err != nil {
			b.Fatal(err)
		}
		encoded = append(encoded, packets...)
	}
	var packets [][]byte
	for _, packet := range encoded {
		parsed, _, err := parsePacket(packet)
		if err != nil {
			b.Fatal(err)
		}
		if parsed.kind == KindData && parsed.index == 3 {
			continue
		}
		packets = append(packets, packet)
	}
	b.SetBytes(DefaultDataShards * int64(len(frame)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		decoder := NewDecoder()
		for _, packet := range packets {
			if _, err := decoder.Handle(time.Now(), packet); err != nil {
				b.Fatal(err)
			}
		}
	}
}
