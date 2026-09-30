package quick

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/platform"
)

type delayedShutdownProcess struct {
	done  chan struct{}
	delay time.Duration
}

func (*delayedShutdownProcess) Start() error { return nil }
func (p *delayedShutdownProcess) Stop() error {
	go func() { time.Sleep(p.delay); close(p.done) }()
	return nil
}
func (p *delayedShutdownProcess) Done() <-chan struct{} { return p.done }
func (*delayedShutdownProcess) Err() error              { return nil }
func (*delayedShutdownProcess) PID() int                { return 17444 }

func TestSlowNetworkCleanupPreservesCoreExitBudget(t *testing.T) {
	process := &delayedShutdownProcess{done: make(chan struct{}), delay: 30 * time.Millisecond}
	cleanup := platform.Cleanup(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	err := shutdownQuickRuntimeWithTimeouts(&testHost{}, "office", &config.Config{}, process, nil, &cleanup, true, nil,
		runLog{logger: log.New(io.Discard, "", 0)}, 20*time.Millisecond, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "platform network cleanup") {
		t.Fatalf("lost original network cleanup error: %v", err)
	}
	if strings.Contains(err.Error(), "wait for wg-quic core") {
		t.Fatalf("network cleanup stole core exit budget: %v", err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("shutdown returned before core exited")
	}
}
