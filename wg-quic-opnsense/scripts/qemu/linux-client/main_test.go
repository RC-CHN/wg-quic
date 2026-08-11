package main

import (
	"testing"
	"time"
)

func TestHoldAfterSuccess(t *testing.T) {
	tests := []struct {
		name         string
		hold         time.Duration
		wantErr      bool
		wantSleeps   int
		wantDuration time.Duration
	}{
		{name: "disabled"},
		{
			name:         "enabled",
			hold:         37 * time.Second,
			wantSleeps:   1,
			wantDuration: 37 * time.Second,
		},
		{name: "negative", hold: -time.Second, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var durations []time.Duration
			err := holdAfterSuccess(test.hold, func(duration time.Duration) {
				durations = append(durations, duration)
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("holdAfterSuccess() error = %v, wantErr %v", err, test.wantErr)
			}
			if len(durations) != test.wantSleeps {
				t.Fatalf("sleep calls = %d, want %d", len(durations), test.wantSleeps)
			}
			if len(durations) == 1 && durations[0] != test.wantDuration {
				t.Fatalf("sleep duration = %s, want %s", durations[0], test.wantDuration)
			}
		})
	}
}
