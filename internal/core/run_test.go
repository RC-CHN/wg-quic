package core

import (
	"bytes"
	"encoding/base64"
	"log"
	"strings"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func TestLogDebugConfigurationOmitsKeys(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: "PRIVATE-SECRET",
			ListenPort: 1234,
		},
		Peers: []config.Peer{{
			PublicKey:    "PUBLIC-VALUE",
			PresharedKey: "PRESHARED-SECRET",
			Endpoint:     "192.0.2.1:51820",
		}},
		Transport: config.DefaultTransport(),
	}
	logDebugConfiguration("wg0.conf", "wg0", cfg)
	got := output.String()
	for _, secret := range []string{"PRIVATE-SECRET", "PRESHARED-SECRET", "PUBLIC-VALUE"} {
		if strings.Contains(got, secret) {
			t.Fatalf("debug output leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "preshared_key_present=true") || !strings.Contains(got, "192.0.2.1:51820") {
		t.Fatalf("debug output omitted safe diagnostics: %s", got)
	}
}

func TestLoadConfigurationUsesSnapshotWithoutReadingSourcePath(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
			FwMark:     123,
		},
		Transport: config.DefaultTransport(),
	}
	snapshot, err := config.MarshalSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadConfiguration(
		"/path/that/must/not/be/read.conf",
		RunOptions{Snapshot: bytes.NewReader(snapshot)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Interface.PrivateKey != cfg.Interface.PrivateKey || got.Interface.FwMark != 123 {
		t.Fatalf("loaded snapshot = %#v, want %#v", got, cfg)
	}
}

func TestDirectCoreConfigurationRejectsIgnoredHooks(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
			PostUp:     []string{"echo should-run"},
		},
		Transport: config.DefaultTransport(),
	}
	err := validateDirectCoreConfiguration(cfg)
	if err == nil || !strings.Contains(err.Error(), "run wg-quic-quick") {
		t.Fatalf("hook validation error = %v", err)
	}
}

func TestSupervisedSnapshotMayContainHooks(t *testing.T) {
	cfg := &config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
			PostUp:     []string{"echo supervised"},
		},
		Transport: config.DefaultTransport(),
	}
	snapshot, err := config.MarshalSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadConfiguration(
		"/path/owned/by/quick.conf",
		RunOptions{Snapshot: bytes.NewReader(snapshot)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Interface.PostUp) != 1 || got.Interface.PostUp[0] != "echo supervised" {
		t.Fatalf("supervised hooks = %#v", got.Interface.PostUp)
	}
}
