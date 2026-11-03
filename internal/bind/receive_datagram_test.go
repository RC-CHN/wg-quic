package armorbind

import (
	"bytes"
	"context"
	"net/netip"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/transport/fec"
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

func receiveDatagramFixture(t testing.TB) (*session, netip.AddrPort) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	owner := New(DefaultConfig())
	remote := netip.MustParseAddrPort("192.0.2.1:443")
	s := &session{
		id: 1, ctx: ctx, endpoint: &Endpoint{owner: owner, addr: remote},
		state:      &runState{ctx: ctx, recv: make(chan receivedPacket, 16), reassembly: newReassembler()},
		fecDecoder: fec.NewDecoder(), control: make(chan []byte, 64),
	}
	s.remoteAddr.Store(&remote)
	return s, remote
}

func protectedReceivePackets(t testing.TB, parity int) [][]byte {
	t.Helper()
	controller := fec.NewController()
	encoder := fec.NewEncoder(4, controller)
	if parity == 2 {
		controller.Observe(fec.Feedback{Total: 4, Missing: 2})
	}
	var packets [][]byte
	for i := range 4 {
		frames, err := fragmentPacket(bytes.Repeat([]byte{byte(i + 1)}, 1000), uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		output, err := encoder.Add(frames[0])
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, output...)
	}
	t.Cleanup(func() {
		for _, p := range packets {
			quiccarrier.ReleaseDatagramSendBuffer(p)
		}
	})
	return packets
}

func BenchmarkReceiveDatagram(b *testing.B) {
	for _, kind := range []string{"feedback", "duplicate", "malformed", "raw1280"} {
		b.Run(kind, func(b *testing.B) {
			s, remote := receiveDatagramFixture(b)
			var packet []byte
			switch kind {
			case "feedback":
				packet = fec.MarshalFeedback(fec.Feedback{Epoch: 1, GroupID: 1, Total: 4})
				b.Cleanup(func() { quiccarrier.ReleaseDatagramSendBuffer(packet) })
			case "duplicate":
				packet = protectedReceivePackets(b, 1)[0]
				s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: packet, RemoteAddr: remote})
				(<-s.state.recv).dispose()
			case "malformed":
				packet = []byte("WGQF")
			case "raw1280":
				frames, err := fragmentPacket(make([]byte, 1280), 1)
				if err != nil {
					b.Fatal(err)
				}
				packet = frames[0]
			}
			b.ReportAllocs()
			for b.Loop() {
				s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: packet, RemoteAddr: remote})
				if kind == "raw1280" {
					(<-s.state.recv).dispose()
				}
			}
		})
	}
}

// Non-delivering carrier traffic must still advance ingress sequencing. A core
// generation barrier taken between datagrams must not accept an old identity.
func TestReceiveDatagramSequenceAcrossControlAndMigration(t *testing.T) {
	s, remote := receiveDatagramFixture(t)
	feedback := fec.MarshalFeedback(fec.Feedback{Epoch: 1, GroupID: 1, Total: 4})
	defer quiccarrier.ReleaseDatagramSendBuffer(feedback)
	s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: feedback, RemoteAddr: remote})
	if len(s.state.recv) != 0 || s.endpoint.owner.ReceiveSequence() != 1 {
		t.Fatal("control changed delivery or sequence")
	}
	barrier := s.endpoint.owner.ReceiveSequence()
	frames, err := fragmentPacket([]byte("authenticated by WireGuard downstream"), 1)
	if err != nil {
		t.Fatal(err)
	}
	s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: frames[0], RemoteAddr: remote})
	first := <-s.state.recv
	defer first.dispose()
	moved := netip.MustParseAddrPort("198.51.100.1:8443")
	s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: frames[0], RemoteAddr: moved})
	second := <-s.state.recv
	defer second.dispose()
	if first.ep.addr != remote || second.ep.addr != moved || s.currentRemoteAddr() != moved {
		t.Fatal("migration mutated outstanding identity")
	}
	if first.ep.ReceiveSequence() != barrier+1 || second.ep.ReceiveSequence() != barrier+2 {
		t.Fatal("incorrect ingress sequencing")
	}
	if first.ep == second.ep {
		t.Fatal("different datagrams share mutable identity")
	}
}

func TestReceiveDatagramFECRecoveryPreservesIngressSnapshot(t *testing.T) {
	s, remote := receiveDatagramFixture(t)
	packets := protectedReceivePackets(t, 1)
	// The first parity shard can recover the omitted final source shard.
	// Deliver all but the final data shard, then parity, preserving FEC semantics.
	var parity []byte
	var data [][]byte
	for _, p := range packets {
		kind, ok := fec.PacketKind(p)
		if !ok {
			t.Fatal("invalid fixture")
		}
		if kind == fec.KindData {
			data = append(data, p)
		} else if kind == fec.KindParity && parity == nil {
			parity = p
		}
	}
	if len(data) != 4 || parity == nil {
		t.Fatal("fixture lacks data or parity")
	}
	for _, p := range data[:3] {
		s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: p, RemoteAddr: remote})
		got := <-s.state.recv
		got.dispose()
	}
	s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: parity, RemoteAddr: remote})
	if len(s.state.recv) != 1 {
		t.Fatalf("recovery delivered %d packets", len(s.state.recv))
	}
	recovered := <-s.state.recv
	defer recovered.dispose()
	if !bytes.Equal(recovered.data, bytes.Repeat([]byte{4}, 1000)) || recovered.ep.ReceiveSequence() != 4 {
		t.Fatal("recovered data or ingress identity changed")
	}
	// A duplicate must not escape; it still consumes an ingress sequence.
	s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: data[3], RemoteAddr: remote})
	if len(s.state.recv) != 0 || s.endpoint.owner.ReceiveSequence() != 5 {
		t.Fatal("late source duplicate changed delivery or sequencing")
	}
}

// Multiple frames recovered by one parity datagram must share its immutable
// path and sequence. Earlier non-delivering parity must still advance ingress.
func TestReceiveDatagramMultipleRecoveredFramesShareSnapshot(t *testing.T) {
	s, remote := receiveDatagramFixture(t)
	packets := protectedReceivePackets(t, 2)
	var data, parity [][]byte
	for _, p := range packets {
		kind, _ := fec.PacketKind(p)
		if kind == fec.KindData {
			data = append(data, p)
		} else if kind == fec.KindParity {
			parity = append(parity, p)
		}
	}
	if len(data) != 4 || len(parity) != 2 {
		t.Fatalf("fixture dimensions: %d+%d", len(data), len(parity))
	}
	for _, p := range data[:2] {
		s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: p, RemoteAddr: remote})
		(<-s.state.recv).dispose()
	}
	s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: parity[0], RemoteAddr: remote})
	if len(s.state.recv) != 0 || s.endpoint.owner.ReceiveSequence() != 3 {
		t.Fatal("insufficient parity changed delivery or ingress sequence")
	}
	s.handleReceivedDatagram(nil, quiccarrier.ReceivedDatagram{Data: parity[1], RemoteAddr: remote})
	if len(s.state.recv) != 2 {
		t.Fatalf("recovered %d packets", len(s.state.recv))
	}
	first, second := <-s.state.recv, <-s.state.recv
	defer first.dispose()
	defer second.dispose()
	if first.ep != second.ep || first.ep.ReceiveSequence() != 4 || first.ep.addr != remote {
		t.Fatal("recovered frames lost triggering datagram identity")
	}
	if !bytes.Equal(first.data, bytes.Repeat([]byte{3}, 1000)) || !bytes.Equal(second.data, bytes.Repeat([]byte{4}, 1000)) {
		t.Fatal("recovered contents changed")
	}
}
