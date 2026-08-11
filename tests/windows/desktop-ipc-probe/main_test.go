//go:build windows

package main

import (
	"encoding/json"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRandomDesktopPipePathMatchesProductionShape(t *testing.T) {
	first, err := randomDesktopPipePath()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomDesktopPipePath()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(
		`^\\\\\.\\pipe\\wg-quic-desktop-[0-9]+-[0-9a-f]{32}$`,
	)
	if !pattern.MatchString(first) {
		t.Fatalf("desktop pipe path %q does not match production shape", first)
	}
	if first == second {
		t.Fatalf("two random desktop pipe paths were identical: %q", first)
	}
}

func TestDesktopCheckRequestCarriesPreElevationDeadline(t *testing.T) {
	now := time.Now()
	request, operationDeadline := newDesktopCheckRequest("office", now)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"deadline_unix_millis":`) {
		t.Fatalf("desktop request omitted deadline: %s", payload)
	}
	deadline := time.UnixMilli(request.DeadlineUnixMillis)
	if !deadline.After(now) {
		t.Fatalf("desktop request deadline %s is not in the future", deadline)
	}
	if deadline.After(now.Add(desktopOperationTimeout + time.Millisecond)) {
		t.Fatalf("desktop request deadline %s exceeds operation timeout", deadline)
	}
	if operationDeadline.UnixMilli() != request.DeadlineUnixMillis {
		t.Fatalf(
			"connection deadline %s differs from request deadline %s",
			operationDeadline,
			deadline,
		)
	}
}

func TestAcceptBeforeExchangesOverDuplexConnection(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	listener := &singleConnectionListener{connection: server}
	accepted, err := acceptBefore(listener, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if accepted != server {
		t.Fatal("acceptBefore returned a different connection")
	}
	peerResult := make(chan string, 1)
	go func() {
		buffer := make([]byte, len("request"))
		if _, err := client.Read(buffer); err != nil {
			peerResult <- "read: " + err.Error()
			return
		}
		if string(buffer) != "request" {
			peerResult <- "request payload = " + string(buffer)
			return
		}
		if _, err := client.Write([]byte("result")); err != nil {
			peerResult <- "write: " + err.Error()
			return
		}
		peerResult <- ""
	}()
	if _, err := accepted.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("result"))
	if _, err := accepted.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "result" {
		t.Fatalf("duplex response = %q, want result", buffer)
	}
	if errText := <-peerResult; errText != "" {
		t.Fatal(errText)
	}
}

type singleConnectionListener struct {
	connection net.Conn
}

func (listener *singleConnectionListener) Accept() (net.Conn, error) {
	return listener.connection, nil
}

func (listener *singleConnectionListener) Close() error {
	return nil
}

func (listener *singleConnectionListener) Addr() net.Addr {
	return listener.connection.LocalAddr()
}
