package fec

import "testing"

func TestEncoderConfiguredInterleaveSurvivesAdd(t *testing.T) {
	for _, reconfigure := range []bool{false, true} {
		c := NewController()
		e := NewEncoder(8, c)
		if reconfigure {
			if _, err := e.Reconfigure(8, 2); err != nil {
				t.Fatal(err)
			}
		} else if err := e.SetInterleave(2); err != nil {
			t.Fatal(err)
		}
		var groups []uint64
		for range 2 {
			packets, err := e.Add([]byte("source"))
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range packets {
				p, _, err := parsePacket(raw)
				if err != nil {
					t.Fatal(err)
				}
				if p.kind == KindData {
					if p.index != 0 {
						t.Fatal("configured lanes collapsed into one group")
					}
					groups = append(groups, p.groupID)
				}
			}
		}
		if len(groups) != 2 || groups[0] == groups[1] {
			t.Fatalf("configured two-way interleave did not emit distinct groups: %v", groups)
		}
	}
}

func TestEncoderReconfigureKeepsAutomaticBurstProtection(t *testing.T) {
	c := NewController()
	e := NewEncoder(32, c)
	c.setParity(4)
	for range 2 {
		c.Observe(Feedback{Total: 8, Missing: 8})
	}
	if c.CurrentInterleave() != 4 {
		t.Fatal("fixture did not enable burst interleaving")
	}
	if _, err := e.Reconfigure(4, 2); err != nil {
		t.Fatal(err)
	}
	if e.interleave != 4 {
		t.Fatal("profile change removed active burst protection")
	}
	for range 64 {
		c.Observe(Feedback{Total: 8})
	}
	if _, err := e.Add([]byte("healthy source")); err != nil {
		t.Fatal(err)
	}
	if e.interleave != 2 {
		t.Fatalf("healthy interleave = %d, want configured minimum 2", e.interleave)
	}
}
