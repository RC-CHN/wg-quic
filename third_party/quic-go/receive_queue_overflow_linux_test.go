//go:build linux

package quic

import (
	"encoding/binary"
	"math"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseReceiveQueueOverflow(t *testing.T) {
	body := make([]byte, 4)
	binary.NativeEndian.PutUint32(body, 17)
	value, ok := parseReceiveQueueOverflow(unix.SOL_SOCKET, unix.SO_RXQ_OVFL, body)
	if !ok || value != 17 {
		t.Fatalf("parseReceiveQueueOverflow() = %d, %v", value, ok)
	}
	if _, ok := parseReceiveQueueOverflow(unix.IPPROTO_IP, unix.SO_RXQ_OVFL, body); ok {
		t.Fatal("accepted receive overflow message at the wrong socket level")
	}
	if _, ok := parseReceiveQueueOverflow(unix.SOL_SOCKET, unix.SO_RXQ_OVFL, body[:3]); ok {
		t.Fatal("accepted receive overflow message with the wrong body size")
	}
}

func TestReceiveQueueOverflowExtendsKernelCounterAcrossWrap(t *testing.T) {
	conn := &oobConn{receiveQueueOverflowSupported: true}
	conn.recordReceiveQueueOverflow(math.MaxUint32 - 2)
	conn.recordReceiveQueueOverflow(1)
	stats := conn.receiveQueueOverflowStats()
	if !stats.Supported || stats.Source != "linux_so_rxq_ovfl" {
		t.Fatalf("stats = %#v", stats)
	}
	if want := uint64(math.MaxUint32) + 2; stats.Packets != want {
		t.Fatalf("packets = %d, want %d", stats.Packets, want)
	}
}
