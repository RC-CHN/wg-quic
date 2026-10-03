package fec

import (
	"testing"
	"time"

	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

func TestControllerSmallGroupBurstRequiresRepeatedEvidence(t *testing.T) {
	c := NewController()
	c.setParity(4)
	severe := Feedback{Total: 2, Missing: 2}
	healthy := Feedback{Total: 2}
	c.Observe(severe)
	if c.CurrentInterleave() != 1 {
		t.Fatal("one short group enabled interleaving")
	}
	c.Observe(healthy)
	c.Observe(severe)
	if c.CurrentInterleave() != 1 {
		t.Fatal("separated short-group failures counted as one burst")
	}
	c.Observe(severe)
	if c.CurrentInterleave() != 2 {
		t.Fatal("repeated loss of entire short groups did not enable interleaving")
	}
	c.Observe(severe)
	if c.CurrentInterleave() != 2 {
		t.Fatal("interleaving increased again without fresh repeated evidence")
	}
	c.Observe(severe)
	if c.CurrentInterleave() != 4 {
		t.Fatal("a repeated short-group burst did not reach maximum interleaving")
	}
}

func TestControllerSmallGroupsDoNotPrematurelyEndBurstProtection(t *testing.T) {
	for _, dataShards := range []int{4, 32} {
		c := NewController()
		c.SetDataShards(dataShards)
		c.setParity(4)
		c.Observe(Feedback{Total: 4, Missing: 4})
		if c.CurrentInterleave() != 2 {
			t.Fatal("fixture did not enter burst protection")
		}
		for range interleaveDecreaseGroups*dataShards - 1 {
			c.Observe(Feedback{Total: 1})
		}
		if c.CurrentInterleave() != 2 {
			t.Fatalf("profile %d: short healthy groups ended protection too soon", dataShards)
		}
		c.Observe(Feedback{Total: 1})
		if c.CurrentInterleave() != 1 {
			t.Fatalf("profile %d: sustained healthy source traffic did not end protection", dataShards)
		}
	}
}

func TestEncoderInterleaveExtendsOnlyParityWindow(t *testing.T) {
	for interleave := 1; interleave <= MaxInterleave; interleave *= 2 {
		c := NewController()
		c.setParity(4)
		c.interleave = interleave
		c.interleaveSnapshot.Store(int32(interleave))
		e := NewEncoder(32, c)
		for i := range interleave * 4 {
			packets, err := e.Add([]byte{byte(i)})
			if err != nil {
				t.Fatal(err)
			}
			if len(packets) != 1 {
				t.Fatalf("interleave %d withheld data or closed the short group early", interleave)
			}
			p, _, err := parsePacket(packets[0])
			if err != nil || p.kind != KindData {
				t.Fatalf("data did not leave immediately: %+v %v", p, err)
			}
			quiccarrier.ReleaseDatagramSendBuffer(packets[0])
		}
		if delay := e.FlushDelay(2 * time.Millisecond); delay != time.Duration(interleave)*2*time.Millisecond || delay > 8*time.Millisecond {
			t.Fatalf("interleave %d: unexpected repair window %v", interleave, delay)
		}
		packets, err := e.Flush()
		if err != nil {
			t.Fatal(err)
		}
		groups := 0
		for _, raw := range packets {
			p, _, err := parsePacket(raw)
			if err != nil {
				t.Fatal(err)
			}
			if p.kind == KindClose {
				groups++
				if p.k != 4 || p.r != 2 {
					t.Fatalf("interleave %d changed per-group protection: %+v", interleave, p)
				}
			}
			quiccarrier.ReleaseDatagramSendBuffer(raw)
		}
		if groups != interleave {
			t.Fatalf("closed %d groups, want %d", groups, interleave)
		}
	}
}

func TestEncoderConfiguredInterleaveKeepsProfileDeadline(t *testing.T) {
	c := NewController()
	e := NewEncoder(32, c)
	if err := e.SetInterleave(2); err != nil {
		t.Fatal(err)
	}
	if got := e.FlushDelay(4 * time.Millisecond); got != 4*time.Millisecond {
		t.Fatalf("configured throughput deadline changed to %v", got)
	}
	c.setParity(4)
	for range 2 {
		c.Observe(Feedback{Total: 8, Missing: 8})
	}
	if _, err := e.Add([]byte("source")); err != nil {
		t.Fatal(err)
	}
	if got := e.FlushDelay(4 * time.Millisecond); got != 8*time.Millisecond {
		t.Fatalf("adaptive deadline = %v, want 8ms", got)
	}
}
