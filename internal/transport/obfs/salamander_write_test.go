package obfs

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestSalamanderWritesWithValueAddresses(t *testing.T) {
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			ip := net.IPv4(127, 0, 0, 1)
			if network == "udp6" {
				ip = net.IPv6loopback
			}
			receiver, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
			if err != nil {
				if network == "udp6" {
					t.Skipf("IPv6 unavailable: %v", err)
				}
				t.Fatal(err)
			}
			defer receiver.Close()
			sender, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			key := Key{42}
			wrapped, err := WrapKeyedSalamander(sender, []PeerKey{{Key: key}})
			if err != nil {
				t.Fatal(err)
			}
			decoder, err := WrapKeyedSalamander(receiver, []PeerKey{{Key: key}})
			if err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte{0xa5}, 1300)
			addr := receiver.LocalAddr().(*net.UDPAddr)
			_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
			for _, message := range []bool{false, true} {
				var n int
				if message {
					n, _, err = wrapped.WriteMsgUDP(payload, nil, addr)
				} else {
					n, err = wrapped.WriteToUDP(payload, addr)
				}
				if err != nil || n != len(payload) {
					t.Fatalf("write = %d, %v", n, err)
				}
				got := make([]byte, len(payload))
				n, _, err = decoder.ReadFromUDP(got)
				if err != nil || !bytes.Equal(got[:n], payload) {
					t.Fatalf("decoded payload differs: %v", err)
				}
			}
		})
	}
}

func BenchmarkSalamanderWriteMsgUDP(b *testing.B) {
	receiver, sender := listenUDP(b), listenUDP(b)
	defer receiver.Close()
	defer sender.Close()
	wrapped, err := WrapKeyedSalamander(sender, []PeerKey{{Key: Key{42}}})
	if err != nil {
		b.Fatal(err)
	}
	addr := receiver.LocalAddr().(*net.UDPAddr)
	payload := bytes.Repeat([]byte{0xa5}, 1300)
	buf := make([]byte, 2048)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		if _, _, err := wrapped.WriteMsgUDP(payload, nil, addr); err != nil {
			b.Fatal(err)
		}
		if _, _, err := receiver.ReadFromUDPAddrPort(buf); err != nil {
			b.Fatal(err)
		}
	}
}
