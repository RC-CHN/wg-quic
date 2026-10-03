//go:build windows

package quick

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
)

// Report when the operation has checked its context before waiting for another
// mutation. Taking the error before signaling makes cancellation deterministic.
type windowsManagementObservedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *windowsManagementObservedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestWindowsManagementCanceledMutationStopsWaiting(t *testing.T) {
	var mutations windowsManagementMutationGate
	if err := mutations.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			mutations.unlock()
		}
	}()
	original := windowsManagementOpenStoredConfig
	defer func() { windowsManagementOpenStoredConfig = original }()
	want := errors.New("configuration opened after the preceding mutation")
	var calls atomic.Int32
	windowsManagementOpenStoredConfig = func(string) (*windowsStoredConfigLease, *config.Config, error) {
		calls.Add(1)
		return nil, nil, want
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &windowsManagementObservedContext{Context: ctx, checked: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := runWindowsManagementOperation(observed, windowsManagementRequest{Action: "up", Name: "queued"}, &mutations)
		result <- err
	}()
	<-observed.checked
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled mutation error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		// Let the old implementation exit too, so a failing regression cannot
		// leave a goroutine referring to the temporary configuration hook.
		mutations.unlock()
		held = false
		<-result
		t.Fatal("canceled mutation kept waiting for another mutation")
	}
	if calls.Load() != 0 {
		t.Fatal("canceled mutation opened its configuration")
	}

	// A canceled waiter must not release the slot held by a different request.
	go func() {
		_, err := runWindowsManagementOperation(context.Background(), windowsManagementRequest{Action: "up", Name: "next"}, &mutations)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("following mutation ran while the preceding mutation held the slot: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := runWindowsManagementOperation(context.Background(), windowsManagementRequest{Action: "probe"}, &mutations); err != nil {
		t.Fatalf("read-only probe waited for a mutation: %v", err)
	}
	mutations.unlock()
	held = false
	select {
	case err := <-result:
		if !errors.Is(err, want) || calls.Load() != 1 {
			t.Fatalf("following mutation error = %v, configuration opens = %d", err, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("following mutation did not acquire the released slot")
	}
}

func TestWindowsManagementMutationRemainsSerialized(t *testing.T) {
	var mutations windowsManagementMutationGate // Exercise lazy, zero-value initialization.
	original := windowsManagementOpenStoredConfig
	defer func() { windowsManagementOpenStoredConfig = original }()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	want := errors.New("configuration unavailable")
	windowsManagementOpenStoredConfig = func(string) (*windowsStoredConfigLease, *config.Config, error) {
		n := active.Add(1)
		for current := maximum.Load(); n > current; current = maximum.Load() {
			if maximum.CompareAndSwap(current, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return nil, nil, want
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := runWindowsManagementOperation(context.Background(), windowsManagementRequest{Action: "up", Name: "office"}, &mutations)
			results <- err
		}()
	}
	<-entered
	select {
	case <-entered:
		close(release)
		<-results
		<-results
		t.Fatal("two mutations entered the configuration operation concurrently")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; !errors.Is(err, want) {
			t.Fatalf("mutation error = %v", err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent mutation operations = %d", maximum.Load())
	}
}
