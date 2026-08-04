//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/quick"
	"golang.org/x/sys/windows/svc"
)

func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func runQuick(ctx context.Context, input, name string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if isService {
		return quick.RunWindowsService(input, name)
	}
	return quick.Run(ctx, input, name)
}

func runQuickDebug(ctx context.Context, input, name string) error {
	file, path, err := quick.CreateWindowsDebugLog(input, name)
	if err != nil {
		return err
	}
	defer file.Close()
	output := &lockedWriter{writer: io.MultiWriter(os.Stdout, file)}
	fmt.Fprintf(output, "wg-quic Windows debug log\n")
	fmt.Fprintf(output, "version=%s go=%s arch=%s started=%s\n", version, runtime.Version(), runtime.GOARCH, time.Now().Format(time.RFC3339Nano))
	fmt.Fprintf(output, "log_path=%s\n", path)
	fmt.Fprintln(output, "secrets: private keys and preshared keys are omitted; abbreviated peer public-key identifiers and network addresses may be included")
	fmt.Printf("debug log: %s\n", path)
	err = quick.RunWindowsDebug(ctx, input, name, output)
	if err != nil {
		fmt.Fprintf(output, "debug run failed: %v\n", err)
	} else {
		fmt.Fprintln(output, "debug run stopped cleanly")
	}
	return err
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}
