//go:build windows

package namedpipe

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAsyncIOSynchronousAbortDuringCloseReturnsClosed(t *testing.T) {
	file := &file{}
	file.closing.Store(true)

	_, err := file.asyncIo(
		&ioOperation{},
		nil,
		0,
		windows.ERROR_OPERATION_ABORTED,
	)
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("synchronous abort during close = %v, want os.ErrClosed", err)
	}
}

func TestAsyncIOSynchronousAbortWithoutCloseIsPreserved(t *testing.T) {
	file := &file{}

	_, err := file.asyncIo(
		&ioOperation{},
		nil,
		0,
		windows.ERROR_OPERATION_ABORTED,
	)
	if !errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
		t.Fatalf("synchronous abort without close = %v", err)
	}
}
