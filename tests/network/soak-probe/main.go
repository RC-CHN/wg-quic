// soak-probe exercises an established tunnel with checked, bidirectional TCP
// traffic. It reconnects the application socket after deadlines, never the VPN.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"time"
)

func main() {
	listen := flag.String("listen", "", "echo server address; bind only to an isolated test interface")
	connect := flag.String("connect", "", "echo server address reached through the tunnel")
	duration := flag.Duration("duration", 10*time.Minute, "client run duration")
	rate := flag.Int("bytes-per-second", 2*1024*1024, "maximum payload rate per direction")
	flag.Parse()
	var err error
	switch {
	case *listen != "" && *connect == "":
		err = serve(*listen)
	case *connect != "" && *listen == "" && *duration > 0 && *rate > 0:
		err = run(*connect, *duration, *rate)
	default:
		err = fmt.Errorf("choose -listen or -connect with positive duration and rate")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			buffer := make([]byte, 64*1024)
			for {
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				n, err := conn.Read(buffer)
				if n > 0 {
					if _, writeErr := io.Copy(conn, bytes.NewReader(buffer[:n])); writeErr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}
}

type counters struct {
	bytes, errors, connections atomic.Uint64
	lastProgress, maxGap       atomic.Int64
}

func run(address string, duration time.Duration, rate int) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var stats counters
	output := json.NewEncoder(os.Stdout)
	report := func(final bool) {
		now := time.Since(start)
		gap := now - time.Duration(stats.lastProgress.Load())
		_ = output.Encode(map[string]any{
			"utc": time.Now().UTC(), "elapsed_seconds": now.Seconds(),
			"verified_bytes_per_direction": stats.bytes.Load(), "socket_errors": stats.errors.Load(),
			"connections": stats.connections.Load(), "seconds_since_delivery": gap.Seconds(),
			"longest_delivery_gap_seconds": time.Duration(max(stats.maxGap.Load(), int64(gap))).Seconds(),
			"final":                        final,
		})
	}
	reporterDone := make(chan struct{})
	go func() {
		defer close(reporterDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				report(false)
			}
		}
	}()
	defer func() { cancel(); <-reporterDone; report(true) }()
	request, reply := make([]byte, 64*1024), make([]byte, 64*1024)
	if _, err := rand.Read(request); err != nil {
		return err
	}
	interval := time.Duration(float64(time.Second) * float64(len(request)) / float64(rate))
	dialer := net.Dialer{Timeout: 2 * time.Second}
	var sequence uint64
	for ctx.Err() == nil {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			stats.errors.Add(1)
			pause(ctx, 100*time.Millisecond)
			continue
		}
		stats.connections.Add(1)
		for ctx.Err() == nil {
			frameStart := time.Now()
			sequence++
			binary.BigEndian.PutUint64(request, sequence)
			deadline := frameStart.Add(2 * time.Second)
			if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
				deadline = end
			}
			_ = conn.SetDeadline(deadline)
			_, err = io.Copy(conn, bytes.NewReader(request))
			if err == nil {
				_, err = io.ReadFull(conn, reply)
			}
			if err != nil {
				stats.errors.Add(1)
				break
			}
			if !bytes.Equal(request, reply) {
				conn.Close()
				return fmt.Errorf("payload mismatch at frame %d", sequence)
			}
			elapsed := int64(time.Since(start))
			gap := elapsed - stats.lastProgress.Swap(elapsed)
			if gap > stats.maxGap.Load() {
				stats.maxGap.Store(gap)
			}
			stats.bytes.Add(uint64(len(request)))
			pause(ctx, interval-time.Since(frameStart))
		}
		conn.Close()
	}
	if stats.bytes.Load() == 0 {
		return fmt.Errorf("no verified data was delivered")
	}
	if time.Since(start)-time.Duration(stats.lastProgress.Load()) > 5*time.Second {
		return fmt.Errorf("traffic did not recover during the last five seconds")
	}
	return nil
}

func pause(ctx context.Context, duration time.Duration) {
	if duration <= 0 {
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
