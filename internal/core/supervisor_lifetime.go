package core

import (
	"context"
	"io"
	"sync"
)

// supervisorLifetimeContext is cancelled when the inherited supervisor pipe
// reaches EOF or otherwise becomes unreadable. Unlike Linux PDEATHSIG, pipe
// lifetime follows the supervisor process rather than the particular Go
// runtime thread that happened to fork the child.
func supervisorLifetimeContext(
	parent context.Context,
	lifetime io.ReadCloser,
) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	var closeOnce sync.Once
	closeLifetime := func() {
		closeOnce.Do(func() { _ = lifetime.Close() })
	}
	go func() {
		_, _ = io.Copy(io.Discard, lifetime)
		cancel()
	}()
	return ctx, func() {
		cancel()
		closeLifetime()
	}
}
