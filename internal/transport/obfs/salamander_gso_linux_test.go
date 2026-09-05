//go:build linux

package obfs

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestSalamanderGSOWritesIndependentWireDatagrams(t *testing.T) {
	receiver, sender := listenUDP(t), listenUDP(t)
	defer receiver.Close()
	defer sender.Close()
	raw, err := sender.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var probeErr error
	if err := raw.Control(func(fd uintptr) {
		_, probeErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_SEGMENT)
	}); err != nil {
		t.Fatal(err)
	}
	if probeErr != nil {
		t.Skipf("kernel has no UDP_SEGMENT support: %v", probeErr)
	}
	key := Key{42}
	wrapped, err := WrapKeyedSalamander(sender, []PeerKey{{Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := WrapKeyedSalamander(receiver, []PeerKey{{Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	// The final segment may be shorter. Every wire segment must have its own
	// Salamander header, and decoding must restore the original boundaries.
	packets := [][]byte{bytes.Repeat([]byte{1}, 1300), bytes.Repeat([]byte{2}, 1300), bytes.Repeat([]byte{3}, 700)}
	payload := bytes.Join(packets, nil)
	oob := make([]byte, unix.CmsgSpace(2))
	header := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	header.Level, header.Type = unix.IPPROTO_UDP, unix.UDP_SEGMENT
	header.SetLen(unix.CmsgLen(2))
	binary.NativeEndian.PutUint16(oob[unix.CmsgLen(0):], 1300)
	remote := receiver.LocalAddr()
	n, _, err := wrapped.WriteMsgUDP(payload, oob, remote.(*net.UDPAddr))
	if err != nil || n != len(payload) {
		t.Fatalf("GSO send = %d, %v", n, err)
	}
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	for _, want := range packets {
		wire := make([]byte, 1500)
		n, _, err := receiver.ReadFromUDPAddrPort(wire)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(want)+SalamanderHeaderSize {
			t.Fatalf("wire packet size = %d, want %d", n, len(want)+SalamanderHeaderSize)
		}
		plain := make([]byte, 1500)
		n, _, ok := decoder.decode(wire[:n], plain)
		if !ok || !bytes.Equal(plain[:n], want) {
			t.Fatal("GSO changed datagram content or order")
		}
	}
}
