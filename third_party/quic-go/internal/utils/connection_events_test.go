package utils

import (
	"slices"
	"testing"
	"time"
)

func TestConnectionEventsCaptureTypedControllerTransitions(t *testing.T) {
	var stats ConnectionStats
	stats.CongestionWindow.Store(12000)
	stats.PropagationRTT.Store((20 * time.Millisecond).Nanoseconds())
	stats.RecordControllerSnapshot("initial")

	stats.PacketsLost.Store(2)
	stats.CongestionWindow.Store(4800)
	stats.CongestionModelState.Store(1)
	stats.PropagationRTT.Store((25 * time.Millisecond).Nanoseconds())
	stats.RecordControllerSnapshot("loss_detection")

	var events []ConnectionEvent
	stats.SetEventObserver(func(event ConnectionEvent) { events = append(events, event) })
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
		if event.Sequence == 0 || event.WallTime.IsZero() || event.Before == nil || event.After == nil {
			t.Fatalf("incomplete controller event = %#v", event)
		}
	}
	if !slices.Equal(types, []string{"loss", "cwnd_reduction", "controller_state", "path_rtt"}) {
		t.Fatalf("controller event types = %#v", types)
	}

	stats.PTOCount.Add(1)
	stats.RecordEvent("pto", "1-RTT")
	if len(events) != 5 || events[4].Type != "pto" || events[4].After.PTOCount != 1 {
		t.Fatalf("live PTO event = %#v", events)
	}
}

func TestConnectionEventHistoryIsBounded(t *testing.T) {
	var stats ConnectionStats
	for range maxConnectionEvents + 5 {
		stats.RecordEvent("pto", "test")
	}
	stats.events.mu.Lock()
	defer stats.events.mu.Unlock()
	if len(stats.events.events) != maxConnectionEvents ||
		stats.events.events[0].Sequence != 6 ||
		stats.events.events[len(stats.events.events)-1].Sequence != maxConnectionEvents+5 {
		t.Fatalf("bounded connection event history = %#v", stats.events.events)
	}
}
