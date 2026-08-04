package armorbind

import (
	"bytes"
	"testing"
	"time"
)

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
		got, err = r.add(time.Now(), 7, f)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, packet) {
		t.Fatal("reassembled packet differs")
	}
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
