package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const snapshotVersion = 1

type snapshotEnvelope struct {
	Version int     `json:"version"`
	Config  *Config `json:"config"`
}

// MarshalSnapshot serializes one already parsed and validated configuration
// for a supervised core process. It is an internal process boundary format,
// not a user configuration format.
func MarshalSnapshot(cfg *Config) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("configuration is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate configuration snapshot: %w", err)
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(snapshotEnvelope{Version: snapshotVersion, Config: cfg}); err != nil {
		return nil, fmt.Errorf("encode configuration snapshot: %w", err)
	}
	return output.Bytes(), nil
}

// ParseSnapshot decodes the internal immutable configuration passed by quick.
// WireGuard configuration text is parsed only by quick; the core merely
// validates and consumes this typed snapshot.
func ParseSnapshot(r io.Reader) (*Config, error) {
	if r == nil {
		return nil, errors.New("configuration snapshot is required")
	}
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var envelope snapshotEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode configuration snapshot: %w", err)
	}
	if envelope.Version != snapshotVersion {
		return nil, fmt.Errorf("unsupported configuration snapshot version %d", envelope.Version)
	}
	if envelope.Config == nil {
		return nil, errors.New("configuration snapshot does not contain a configuration")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("configuration snapshot contains trailing data")
		}
		return nil, fmt.Errorf("decode trailing configuration snapshot data: %w", err)
	}
	if err := envelope.Config.Validate(); err != nil {
		return nil, fmt.Errorf("validate configuration snapshot: %w", err)
	}
	return envelope.Config, nil
}
