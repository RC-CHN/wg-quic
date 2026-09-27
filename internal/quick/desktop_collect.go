package quick

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/observe"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

const desktopArchiveLimit = 256 << 10

type desktopObservationClient struct{}

func (desktopObservationClient) Status(ctx context.Context, name string) (management.Status, error) {
	return RuntimeStatus(ctx, name)
}
func (desktopObservationClient) Events(ctx context.Context, name, stream string, after uint64, limit int) (telemetry.SessionEventBatch, error) {
	return RuntimeEvents(ctx, name, stream, after, limit)
}

func validateDesktopCollectPeer(peer string) error {
	raw, err := base64.StdEncoding.DecodeString(peer)
	if err != nil || len(raw) != 32 {
		return errors.New("collect requires a complete WireGuard peer public key")
	}
	return nil
}

// CollectDesktopDiagnostics uses the same generation-aware collector as the
// CLI. Privilege is used only to read management telemetry. No caller-selected
// path is opened by the privileged process; the desktop saves the result.
func CollectDesktopDiagnostics(ctx context.Context, name, peer string) (string, error) {
	if err := platform.Current().ValidateInterfaceName(name); err != nil {
		return "", err
	}
	if err := validateDesktopCollectPeer(peer); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp("", "wg-quic-diagnostics-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(directory)
	output := filepath.Join(directory, "collection")
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	_, collectionErr := observe.Run(ctx, desktopObservationClient{}, observe.Options{
		Interface: name, PeerPublicKey: peer, Duration: 10 * time.Second,
		Interval: 250 * time.Millisecond, MaxBytes: 1 << 20, Output: output, Version: "desktop",
	})
	return encodeDesktopCollection(output, collectionErr)
}

func encodeDesktopCollection(directory string, collectionErr error) (string, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	files := 0
	for _, name := range []string{"manifest.json", "status.ndjson", "controller-events.ndjson", "peer-telemetry.csv", "summary.json", "COMPLETE", "INCOMPLETE"} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return "", fmt.Errorf("invalid collection artifact %s", name)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		entry, err := archive.Create(name)
		if err != nil {
			return "", err
		}
		if _, err := entry.Write(contents); err != nil {
			return "", err
		}
		files++
	}
	if err := archive.Close(); err != nil {
		return "", err
	}
	if files == 0 {
		return "", fmt.Errorf("no diagnostic artifacts: %w", collectionErr)
	}
	if buffer.Len() > desktopArchiveLimit {
		return "", errors.New("diagnostic archive exceeds the desktop export limit; use CLI collect for larger captures")
	}
	// Integer bytes keep the native bridge dependency-free. Even worst-case
	// JSON expansion stays below its existing 2 MiB command-output limit.
	result := struct {
		Complete bool   `json:"complete"`
		Detail   string `json:"detail,omitempty"`
		Archive  []int  `json:"archive"`
	}{Complete: collectionErr == nil, Archive: make([]int, buffer.Len())}
	if collectionErr != nil {
		result.Detail = collectionErr.Error()
	}
	for i, value := range buffer.Bytes() {
		result.Archive[i] = int(value)
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
