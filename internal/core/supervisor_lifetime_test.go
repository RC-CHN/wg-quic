package core

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSupervisorLifetimeContextCancelsOnPipeEOF(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := supervisorLifetimeContext(context.Background(), reader)
	defer stop()

	select {
	case <-ctx.Done():
		t.Fatal("supervisor context ended while its writer was still open")
	case <-time.After(20 * time.Millisecond):
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("supervisor context did not end after pipe EOF")
	}
}
