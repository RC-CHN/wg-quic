package armorbind

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestReassemblyFullCapacityStillCompletesExistingPackets(t *testing.T) {
	r := newReassembler()
	now := time.Unix(1, 0)
	for id := range 2048 {
		if _, err := r.add(now, 1, fragment{packetID: uint64(id), count: 2, total: 2, data: []byte{1}}, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.add(now, 1, fragment{packetID: 2048, count: 2, total: 2, data: []byte{1}}, false); err == nil {
		t.Fatal("new packet admitted beyond the reassembly limit")
	}
	packet, err := r.add(now, 1, fragment{packetID: 0, index: 1, count: 2, total: 2, data: []byte{2}}, false)
	if err != nil {
		t.Fatalf("existing packet cannot complete while the table is full: %v", err)
	}
	defer releaseReassemblyBuffer(packet)
	if !bytes.Equal(packet, []byte{1, 2}) {
		t.Fatalf("reassembled packet = %x", packet)
	}
	if _, err := r.add(now, 1, fragment{packetID: 2048, count: 2, total: 2, data: []byte{1}}, false); err != nil {
		t.Fatalf("completed packet did not free capacity: %v", err)
	}
}

func TestReassemblyRejectsPayloadBeyondDeclaredTotal(t *testing.T) {
	r := newReassembler()
	now := time.Unix(1, 0)
	if _, err := r.add(now, 1, fragment{packetID: 1, count: 3, total: 6, data: []byte{1, 2, 3}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.add(now, 1, fragment{packetID: 1, index: 1, count: 3, total: 6, data: []byte{4, 5, 6, 7}}, false); err == nil {
		t.Fatal("retained more bytes than the packet's declared total")
	}
	if len(r.groups) != 0 {
		t.Fatal("invalid packet retained its earlier shards")
	}
}

func TestReassemblyExpiresIdleAndClosedSessions(t *testing.T) {
	r := newReassembler()
	now := time.Unix(1, 0)
	// Deliberately insert timestamps out of order: concurrent receive loops
	// can read their timestamp before another loop acquires the lock.
	for _, entry := range []struct {
		id uint64
		at time.Time
	}{{1, now.Add(time.Second)}, {2, now}, {3, now.Add(2 * time.Second)}} {
		if _, err := r.add(entry.at, entry.id, fragment{packetID: 1, count: 2, total: 2, data: []byte{1}}, false); err != nil {
			t.Fatal(err)
		}
	}
	r.expire(now.Add(reassemblyTTL))
	if len(r.groups) != 3 {
		t.Fatal("packet expired at the exact TTL boundary")
	}
	r.expire(now.Add(reassemblyTTL + time.Nanosecond))
	if len(r.groups) != 2 || r.groups[reassemblyKey{sessionID: 2, packetID: 1}] != nil {
		t.Fatal("idle expiration did not release the oldest packet")
	}
	r.discardSession(1)
	if len(r.groups) != 1 || len(r.expiry) != 1 {
		t.Fatal("closed session retained shards or expiration entries")
	}
	r.expire(now.Add(10 * time.Second))
	if len(r.groups) != 0 || len(r.expiry) != 0 {
		t.Fatal("expiration retained inactive packet state")
	}
}

func TestReassemblyConcurrentSessions(t *testing.T) {
	r := newReassembler()
	var workers sync.WaitGroup
	for id := range uint64(16) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer r.discardSession(id)
			for packetID := range uint64(1000) {
				now := time.Now()
				for index := range uint16(2) {
					packet, err := r.add(now, id, fragment{packetID: packetID, index: index, count: 2, total: 2, data: []byte{byte(index)}}, false)
					if err != nil {
						t.Errorf("session %d: %v", id, err)
						return
					}
					if index == 1 && !bytes.Equal(packet, []byte{0, 1}) {
						t.Errorf("session %d packet %d: incorrect output %x", id, packetID, packet)
						return
					}
					releaseReassemblyBuffer(packet)
				}
				r.expire(now)
			}
		}()
	}
	workers.Wait()
	if len(r.groups) != 0 || len(r.expiry) != 0 {
		t.Fatal("completed sessions retained reassembly state")
	}
}

func BenchmarkReassemblyUnderLoss(b *testing.B) {
	for _, pending := range []int{1, 128, 2046} {
		b.Run(fmt.Sprint(pending), func(b *testing.B) {
			r := newReassembler()
			now := time.Unix(1, 0)
			for id := range pending {
				if _, err := r.add(now, 1, fragment{packetID: uint64(id), count: 2, total: 2, data: []byte{1}}, false); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				id := uint64(i + pending)
				if _, err := r.add(now, 1, fragment{packetID: id, count: 2, total: 2, data: []byte{1}}, false); err != nil {
					b.Fatal(err)
				}
				packet, err := r.add(now, 1, fragment{packetID: id, index: 1, count: 2, total: 2, data: []byte{2}}, false)
				if err != nil {
					b.Fatal(err)
				}
				releaseReassemblyBuffer(packet)
			}
		})
	}
}
