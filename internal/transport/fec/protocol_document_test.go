package fec

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestStandaloneProtocolFECVectors(t *testing.T) {
	document, err := os.ReadFile("../../../docs/WG-QUIC-PROTOCOL.md")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		FEC struct {
			Frames, Shards, Data, Parity []string
			Close, Feedback              string
		}
	}
	block := bytes.SplitN(document, []byte("```json\n"), 2)
	if len(block) != 2 {
		t.Fatal("missing protocol vectors")
	}
	if err := json.Unmarshal(bytes.SplitN(block[1], []byte("\n```"), 2)[0], &vectors); err != nil {
		t.Fatal(err)
	}
	v := vectors.FEC
	encoder := NewEncoder(3, nil)
	codec, err := cachedCodec(encoder.codecs, 3, 2, 26)
	if err != nil {
		t.Fatal(err)
	}
	shards := make([][]byte, 5)
	for i, value := range v.Shards {
		shards[i] = goldenHex(t, value)
	}
	for i := 3; i < 5; i++ {
		shards[i] = make([]byte, len(shards[0]))
	}
	if err := codec.Encode(shards); err != nil {
		t.Fatal(err)
	}
	for i, value := range v.Parity {
		parsed, _, err := parsePacket(goldenHex(t, value))
		if err != nil || !bytes.Equal(parsed.payload, shards[3+i]) {
			t.Fatalf("parity %d mismatch: %v", i, err)
		}
	}
	decoder := NewDecoder()
	now := time.Unix(1, 0)
	var frames [][]byte
	for _, value := range []string{v.Data[0], v.Parity[0], v.Parity[1]} {
		result, err := decoder.Handle(now, goldenHex(t, value))
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, result.Frames...)
	}
	if len(frames) != 3 {
		t.Fatalf("recovered %d frames", len(frames))
	}
	for i, value := range v.Frames {
		if !bytes.Equal(frames[i], goldenHex(t, value)) {
			t.Fatalf("frame %d mismatch", i)
		}
	}
	feedback := decoder.Expire(now.Add(11 * time.Millisecond))
	if len(feedback) != 1 || !bytes.Equal(MarshalFeedback(feedback[0]), goldenHex(t, v.Feedback)) {
		t.Fatalf("feedback mismatch: %+v", feedback)
	}
}
