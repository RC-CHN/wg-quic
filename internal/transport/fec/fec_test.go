package fec

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

func TestWGQFWireGoldenVectors(t *testing.T) {
	t.Run("data", func(t *testing.T) {
		packet := packet{
			kind:    KindData,
			epoch:   0x1234,
			groupID: 0x0102030405060708,
			index:   2,
			payload: goldenHex(t, "0016574751310101020304050607080000000100000001aa"),
		}
		// WGQF v1 data packet. The payload starts with a two-byte source-frame
		// length (22), followed by a complete one-byte-payload WGQ1 frame.
		want := goldenHex(t, "5747514601001234010203040506070800020000000000180016574751310101020304050607080000000100000001aa")
		got := marshalPacket(packet)
		if !bytes.Equal(got, want) {
			t.Fatalf("WGQF data wire bytes = %x, want %x", got, want)
		}

		parsed, handled, err := parsePacket(want)
		if err != nil || !handled {
			t.Fatalf("parse golden data: handled=%v err=%v", handled, err)
		}
		if parsed.kind != KindData || parsed.epoch != 0x1234 || parsed.groupID != 0x0102030405060708 ||
			parsed.index != 2 || parsed.k != 0 || parsed.r != 0 {
			t.Fatalf("parsed WGQF data header = %#v", parsed)
		}
		if !bytes.Equal(parsed.payload, goldenHex(t, "0016574751310101020304050607080000000100000001aa")) {
			t.Fatalf("parsed WGQF data payload = %x", parsed.payload)
		}

		result, err := NewDecoder().Handle(time.Unix(0, 0), want)
		if err != nil {
			t.Fatal(err)
		}
		wantFrame := goldenHex(t, "574751310101020304050607080000000100000001aa")
		if !result.Handled || len(result.Frames) != 1 || !bytes.Equal(result.Frames[0], wantFrame) {
			t.Fatalf("decoded WGQF data frames = %x", result.Frames)
		}
	})

	t.Run("feedback", func(t *testing.T) {
		feedback := Feedback{
			Epoch: 0xabcd, GroupID: 0x1020304050607080,
			Missing: 3, Total: 8, Recovered: 2,
		}
		// WGQF v1 feedback carries missing/total/recovered in the common
		// index/k/r fields and has no payload.
		want := goldenHex(t, "574751460103abcd10203040506070800003000800020000")
		got := MarshalFeedback(feedback)
		if !bytes.Equal(got, want) {
			t.Fatalf("WGQF feedback wire bytes = %x, want %x", got, want)
		}

		parsed, handled, err := parsePacket(want)
		if err != nil || !handled {
			t.Fatalf("parse golden feedback: handled=%v err=%v", handled, err)
		}
		if parsed.kind != KindFeedback || parsed.epoch != feedback.Epoch || parsed.groupID != feedback.GroupID ||
			parsed.index != feedback.Missing || parsed.k != feedback.Total || parsed.r != feedback.Recovered || len(parsed.payload) != 0 {
			t.Fatalf("parsed WGQF feedback header = %#v", parsed)
		}

		result, err := NewDecoder().Handle(time.Unix(0, 0), want)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Handled || result.ObservedFeedback == nil || *result.ObservedFeedback != feedback {
			t.Fatalf("decoded WGQF feedback = %#v", result.ObservedFeedback)
		}
	})
}

func goldenHex(t testing.TB, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

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
	now := time.Now()
	var got [][]byte
	for i, packet := range packets {
		p, handled, err := parsePacket(packet)
		if err != nil || !handled {
			t.Fatalf("parse packet: handled=%v err=%v", handled, err)
		}
		if p.kind == KindData && p.index == 1 {
			continue
		}
		result, err := decoder.Handle(now, packet)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		got = append(got, result.Frames...)
	}
	feedbackList := decoder.Expire(now.Add(completionGrace + time.Millisecond))
	var feedback *Feedback
	if len(feedbackList) != 0 {
		feedback = &feedbackList[len(feedbackList)-1]
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

func TestCompletionGraceAbsorbsReorderedShard(t *testing.T) {
	now := time.Now()
	controller := NewController()
	encoder := NewEncoder(2, controller)
	var packets [][]byte
	for _, frame := range [][]byte{[]byte("a"), []byte("b")} {
		out, err := encoder.Add(frame)
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, out...)
	}

	decoder := NewDecoder()
	var late []byte
	for _, pkt := range packets {
		p, handled, err := parsePacket(pkt)
		if err != nil || !handled {
			t.Fatalf("parse packet: handled=%v err=%v", handled, err)
		}
		if p.kind == KindData && p.index == 1 {
			late = pkt
			continue
		}
		if _, err := decoder.Handle(now, pkt); err != nil {
			t.Fatal(err)
		}
	}
	// data[1] was reconstructed when close finalized the group; now it
	// arrives late and must walk the loss accounting back to zero.
	if _, err := decoder.Handle(now, late); err != nil {
		t.Fatal(err)
	}
	feedback := decoder.Expire(now.Add(completionGrace + time.Millisecond))
	if len(feedback) != 1 {
		t.Fatalf("feedback = %#v, want one entry", feedback)
	}
	if feedback[0].Missing != 0 || feedback[0].Recovered != 0 {
		t.Fatalf("feedback = %#v, want reorder walked back to zero", feedback[0])
	}
}

func TestCompletionGraceDeliversLateShard(t *testing.T) {
	now := time.Now()
	decoder := NewDecoder()
	// data[0] delivers immediately.
	if _, err := decoder.Handle(now, marshalPacket(packet{
		kind: KindData, epoch: 1, groupID: 1, index: 0, payload: []byte{0, 1, 'a'},
	})); err != nil {
		t.Fatal(err)
	}
	// close with k=2, r=0 finalizes with one missing shard.
	if _, err := decoder.Handle(now, marshalPacket(packet{
		kind: KindClose, epoch: 1, groupID: 1, k: 2, r: 0,
	})); err != nil {
		t.Fatal(err)
	}
	// data[1] arrives late and is delivered now.
	result, err := decoder.Handle(now, marshalPacket(packet{
		kind: KindData, epoch: 1, groupID: 1, index: 1, payload: []byte{0, 1, 'b'},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Frames) != 1 || string(result.Frames[0]) != "b" {
		t.Fatalf("late frames = %#v, want b", result.Frames)
	}
	feedback := decoder.Expire(now.Add(completionGrace + time.Millisecond))
	if len(feedback) != 1 || feedback[0].Missing != 0 || feedback[0].Recovered != 0 {
		t.Fatalf("feedback = %#v, want zero missing after late delivery", feedback)
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

func TestControllerTightensBypassThresholdOnLongRTTPath(t *testing.T) {
	controller := NewController()
	controller.setParity(0)
	controller.ObservePathRTT(600 * time.Millisecond)

	if controller.ObserveTransport(100, 0) {
		t.Fatal("initial transport snapshot changed parity")
	}
	if !controller.ObserveTransport(1100, 1) {
		t.Fatal("long-RTT loss did not leave the zero-parity fast path")
	}
	if got := controller.CurrentParity(); got != 1 {
		t.Fatalf("parity after long-RTT loss = %d, want 1", got)
	}
}

func TestControllerKeepsLowRTTPathBypassedAtSameLossRate(t *testing.T) {
	controller := NewController()
	controller.setParity(0)
	controller.ObservePathRTT(10 * time.Millisecond)

	controller.ObserveTransport(100, 0)
	if controller.ObserveTransport(1100, 1) {
		t.Fatal("low-RTT path enabled parity below the default loss threshold")
	}
	if got := controller.CurrentParity(); got != 0 {
		t.Fatalf("parity after low-RTT loss = %d, want 0", got)
	}
}

func TestControllerDoesNotDropBelowLongRTTLossTarget(t *testing.T) {
	controller := NewController()
	controller.ObservePathRTT(600 * time.Millisecond)
	controller.lossEWMA = 0.001

	for range 32 {
		controller.Observe(Feedback{Total: 8})
	}

	if got := controller.CurrentParity(); got != 1 {
		t.Fatalf("parity after long-RTT zero-loss streak = %d, want 1", got)
	}
}

func TestControllerUsesLongerDecreaseWindowOnLongRTTPath(t *testing.T) {
	controller := NewController()
	controller.ObservePathRTT(600 * time.Millisecond)
	window := controller.decreaseWindowLocked(0)

	for range window - 1 {
		controller.Observe(Feedback{Total: 8})
	}
	if got := controller.CurrentParity(); got != 1 {
		t.Fatalf("parity decreased before long-RTT window: %d", got)
	}
	controller.Observe(Feedback{Total: 8})
	if got := controller.CurrentParity(); got != 0 {
		t.Fatalf("parity after long-RTT window = %d, want 0", got)
	}
}

func TestControllerRemovesSurplusParityNormallyOnLongRTTPath(t *testing.T) {
	controller := NewController()
	controller.setParity(2)
	controller.ObservePathRTT(600 * time.Millisecond)

	for range defaultDecreaseGroups {
		controller.Observe(Feedback{Total: 8})
	}

	if got := controller.CurrentParity(); got != 1 {
		t.Fatalf("surplus parity after normal decrease window = %d, want 1", got)
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
					packets, err := encoder.Add(frame)
					if err != nil {
						b.Fatal(err)
					}
					// Simulate quic-go recycling serialized frames so the
					// benchmark measures the steady-state pool, not misses.
					for _, packet := range packets {
						quiccarrier.ReleaseDatagramSendBuffer(packet)
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
