package armorbind

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/fec"
)

func TestWGQ1WireGoldenVector(t *testing.T) {
	packet := goldenHex(t, "010203040506070809")
	frames, err := fragmentPacketSized(packet, 0x0102030405060708, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("fragment count = %d, want 3", len(frames))
	}

	// WGQ1 v1, packet ID 0x0102030405060708, fragment 2 of 3,
	// total datagram length 9, followed by the final three payload bytes.
	want := goldenHex(t, "574751310101020304050607080002000300000009070809")
	if !bytes.Equal(frames[2], want) {
		t.Fatalf("WGQ1 wire bytes = %x, want %x", frames[2], want)
	}

	got, err := parseFragment(want)
	if err != nil {
		t.Fatal(err)
	}
	if got.packetID != 0x0102030405060708 || got.index != 2 || got.count != 3 || got.total != 9 {
		t.Fatalf("parsed WGQ1 header = %#v", got)
	}
	if !bytes.Equal(got.data, goldenHex(t, "070809")) {
		t.Fatalf("parsed WGQ1 payload = %x", got.data)
	}
}

func goldenHex(t testing.TB, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestFragmentReassembly(t *testing.T) {
	packet := make([]byte, 4097)
	for i := range packet {
		packet[i] = byte(i)
	}
	frames, err := fragmentPacket(packet, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 2 {
		t.Fatal("large packet was not fragmented")
	}
	r := newReassembler()
	var got []byte
	for i := len(frames) - 1; i >= 0; i-- {
		f, err := parseFragment(frames[i])
		if err != nil {
			t.Fatal(err)
		}
		got, err = r.add(time.Now(), 7, f, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, packet) {
		t.Fatal("reassembled packet differs")
	}
}

func TestCommonWireGuardPacketFitsOneProtectedQUICDatagram(t *testing.T) {
	// A 1280-byte inner packet has 32 bytes of WireGuard transport overhead.
	packet := make([]byte, 1312)
	frames, err := fragmentPacketSized(packet, 42, 1365)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("1280-MTU WireGuard packet produced %d fragments, want 1", len(frames))
	}
	encoder := fec.NewEncoder(fec.DefaultDataShards, fec.NewController())
	encoded, err := encoder.Add(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 1 || len(encoded[0]) > 1412 {
		t.Fatalf("protected frame lengths = %v, QUIC DATAGRAM limit 1412", packetLengths(encoded))
	}
}

func TestPreparedPacketReusesQueueBuffer(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, 1312)
	prepared := make([]byte, frameHeaderSize+len(payload))
	copy(prepared[frameHeaderSize:], payload)
	start := &prepared[0]

	frame, err := framePreparedPacket(prepared, 42)
	if err != nil {
		t.Fatal(err)
	}
	if &frame[0] != start {
		t.Fatal("prepared packet allocated a different frame buffer")
	}
	fragment, err := parseFragment(frame)
	if err != nil {
		t.Fatal(err)
	}
	if fragment.packetID != 42 || fragment.index != 0 || fragment.count != 1 {
		t.Fatalf("prepared fragment metadata = %#v", fragment)
	}
	if !bytes.Equal(fragment.data, payload) {
		t.Fatal("prepared fragment payload changed")
	}
}

func TestPreparedPacketRejectsInvalidPayloadSize(t *testing.T) {
	for _, frame := range [][]byte{
		make([]byte, frameHeaderSize),
		make([]byte, frameHeaderSize+maxDatagramSize+1),
	} {
		if _, err := framePreparedPacket(frame, 42); err == nil {
			t.Fatalf("prepared frame length %d was accepted", len(frame))
		}
	}
}

var benchmarkFrame []byte
var benchmarkReassembled []byte

func BenchmarkReassemblerSingleFragment(b *testing.B) {
	payload := bytes.Repeat([]byte{0x5a}, 1312)
	frame := make([]byte, frameHeaderSize+len(payload))
	writeFragmentHeader(frame, 1, 0, 1, uint32(len(payload)))
	copy(frame[frameHeaderSize:], payload)
	fragment, err := parseFragment(frame)
	if err != nil {
		b.Fatal(err)
	}
	for _, owned := range []bool{false, true} {
		name := "copied"
		if owned {
			name = "owned"
		}
		b.Run(name, func(b *testing.B) {
			reassembler := newReassembler()
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				packet, err := reassembler.add(time.Now(), 1, fragment, owned)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkReassembled = packet
			}
		})
	}
}

func BenchmarkFrameCommonWireGuardPacket(b *testing.B) {
	payload := bytes.Repeat([]byte{0x5a}, 1312)
	b.Run("copy-then-frame", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			queued := append([]byte(nil), payload...)
			frames, err := fragmentPacketSized(queued, 42, 1365)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkFrame = frames[0]
		}
	})
	b.Run("copy-with-header-reserve", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			prepared := make([]byte, frameHeaderSize+len(payload))
			copy(prepared[frameHeaderSize:], payload)
			frame, err := framePreparedPacket(prepared, 42)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkFrame = frame
		}
	})
}

func packetLengths(packets [][]byte) []int {
	lengths := make([]int, len(packets))
	for i, packet := range packets {
		lengths[i] = len(packet)
	}
	return lengths
}

func TestRejectMalformedFrame(t *testing.T) {
	if _, err := parseFragment([]byte("not a wg-quic frame")); err == nil {
		t.Fatal("malformed frame was accepted")
	}
}

func TestWireGuardControlPriority(t *testing.T) {
	for _, messageType := range []byte{1, 2, 3} {
		packet := make([]byte, 148)
		packet[0] = messageType
		if !priorityWireGuardDatagram(packet) {
			t.Errorf("WireGuard message type %d was not prioritized", messageType)
		}
	}
	keepalive := make([]byte, 32)
	keepalive[0] = 4
	if !priorityWireGuardDatagram(keepalive) {
		t.Fatal("WireGuard keepalive was not prioritized")
	}
	data := make([]byte, 64)
	data[0] = 4
	if priorityWireGuardDatagram(data) {
		t.Fatal("ordinary WireGuard transport data was prioritized")
	}
}
