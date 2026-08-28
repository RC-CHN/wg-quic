// Package observe implements bounded, generation-aware field telemetry
// collection through the portable privileged management protocol.
package observe

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/management"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
)

const (
	DefaultDuration = 30 * time.Second
	DefaultInterval = 100 * time.Millisecond
	DefaultMaxBytes = int64(16 << 20)
	maxDuration     = 10 * time.Minute
	minInterval     = 10 * time.Millisecond
	maxInterval     = time.Minute
	minMaxBytes     = int64(64 << 10)
	maxMaxBytes     = int64(256 << 20)
	eventPageSize   = 1024
	maxEventPages   = 64
)

type Client interface {
	Status(context.Context, string) (management.Status, error)
	Events(context.Context, string, string, uint64, int) (telemetry.SessionEventBatch, error)
}

type Options struct {
	Interface     string
	PeerPublicKey string
	SessionID     uint64
	Duration      time.Duration
	Interval      time.Duration
	MaxBytes      int64
	Output        string
	Version       string

	// sampleLimit makes deterministic package tests possible. Production
	// callers leave it at zero and use Duration as the hard bound.
	sampleLimit int
}

type Manifest struct {
	SchemaVersion        int       `json:"schema_version"`
	Interface            string    `json:"interface"`
	PeerPublicKey        string    `json:"peer_public_key"`
	SessionID            uint64    `json:"session_id,omitempty"`
	StartedAt            time.Time `json:"started_at"`
	DurationMillis       int64     `json:"duration_millis"`
	IntervalMillis       int64     `json:"interval_millis"`
	MaxBytes             int64     `json:"max_bytes"`
	Version              string    `json:"version"`
	SourceCommit         string    `json:"source_commit,omitempty"`
	SourceModified       bool      `json:"source_modified,omitempty"`
	GOOS                 string    `json:"goos"`
	GOARCH               string    `json:"goarch"`
	GoVersion            string    `json:"go_version"`
	Artifacts            []string  `json:"artifacts"`
	RequiredCapabilities []string  `json:"required_capabilities"`
	Redaction            string    `json:"redaction"`
}

type Summary struct {
	SchemaVersion   int       `json:"schema_version"`
	CompletedAt     time.Time `json:"completed_at"`
	Samples         uint64    `json:"samples"`
	FailedSamples   uint64    `json:"failed_samples"`
	Events          uint64    `json:"events"`
	BytesWritten    int64     `json:"bytes_written"`
	SupervisorEpoch string    `json:"supervisor_epoch"`
	EventStreamID   string    `json:"event_stream_id"`
}

type rawStatusSample struct {
	SampleSequence uint64             `json:"sample_sequence"`
	SampledAt      time.Time          `json:"sampled_at"`
	Status         *management.Status `json:"status,omitempty"`
	Error          string             `json:"error,omitempty"`
}

type collectedEvent struct {
	SupervisorEpoch string `json:"supervisor_epoch"`
	telemetry.SessionEvent
}

type seriesKey struct {
	Epoch      string
	SessionID  uint64
	Generation uint64
}

type selectedSession struct {
	key                seriesKey
	role               string
	state              string
	configuredEndpoint string
	currentEndpoint    string
	authenticated      bool
	configured         bool
	stats              telemetry.SessionStats
}

type collector struct {
	client  Client
	options Options
	started time.Time
	budget  *byteBudget

	statusFile *os.File
	eventFile  *os.File
	csvFile    *os.File
	csv        *csv.Writer

	epoch              string
	eventStreamID      string
	eventCursor        uint64
	current            *selectedSession
	previousStats      telemetry.SessionStats
	havePrevious       bool
	finalized          map[seriesKey]bool
	targetSeries       map[seriesKey]bool
	overflow           uint64
	haveOverflow       bool
	eventClockOffsetNS int64
	haveEventClock     bool

	samples       uint64
	failedSamples uint64
	events        uint64
	issues        []string
	closed        bool
}

func Run(ctx context.Context, client Client, options Options) (result Summary, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return Summary{}, errors.New("observation client is required")
	}
	options, err = normalizeOptions(options)
	if err != nil {
		return Summary{}, err
	}
	absOutput, err := filepath.Abs(options.Output)
	if err != nil {
		return Summary{}, fmt.Errorf("resolve output path: %w", err)
	}
	options.Output = absOutput
	if err := makeSecureOutputDir(absOutput); err != nil {
		return Summary{}, err
	}
	c := &collector{
		client: client, options: options, started: time.Now(),
		budget:    &byteBudget{limit: options.MaxBytes},
		finalized: make(map[seriesKey]bool), targetSeries: make(map[seriesKey]bool),
	}
	defer func() {
		closeErr := c.close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			_ = writeMarker(absOutput, "INCOMPLETE")
		}
	}()
	if err = c.open(); err != nil {
		return Summary{}, err
	}
	if err = c.writeManifest(); err != nil {
		return Summary{}, err
	}
	bootstrap, statusErr := client.Status(ctx, options.Interface)
	if statusErr != nil {
		_ = c.writeRawStatus(nil, statusErr)
		return Summary{}, fmt.Errorf("read initial runtime status: %w", statusErr)
	}
	if err = c.bootstrap(ctx, bootstrap); err != nil {
		_ = c.writeRawStatus(&bootstrap, err)
		return Summary{}, err
	}

	ticker := time.NewTicker(options.Interval)
	defer ticker.Stop()
	deadline := time.NewTimer(options.Duration)
	defer deadline.Stop()
	for {
		if sampleErr := c.sample(ctx); sampleErr != nil {
			if isBoundaryError(sampleErr) || errors.Is(sampleErr, errOutputLimit) {
				return Summary{}, sampleErr
			}
			c.recordIssue(sampleErr)
		}
		if options.sampleLimit > 0 && int(c.samples) >= options.sampleLimit {
			break
		}
		select {
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		case <-deadline.C:
			break
		case <-ticker.C:
			continue
		}
		break
	}
	if err = c.drainEvents(ctx, true); err != nil {
		return Summary{}, err
	}
	result = Summary{
		SchemaVersion: 1, CompletedAt: time.Now(), Samples: c.samples,
		FailedSamples: c.failedSamples, Events: c.events,
		BytesWritten: c.budget.writtenBytes(), SupervisorEpoch: c.epoch,
		EventStreamID: c.eventStreamID,
	}
	if err = c.writeJSONFile("summary.json", result); err != nil {
		return Summary{}, err
	}
	if len(c.issues) != 0 {
		return result, fmt.Errorf(
			"collection completed with %d failed samples: %s",
			c.failedSamples, strings.Join(c.issues, "; "),
		)
	}
	if err = c.close(); err != nil {
		return Summary{}, err
	}
	if err = writeMarker(absOutput, "COMPLETE"); err != nil {
		return Summary{}, err
	}
	return result, nil
}

func normalizeOptions(options Options) (Options, error) {
	if options.Interface == "" {
		return options, errors.New("interface is required")
	}
	if options.PeerPublicKey == "" {
		return options, errors.New("peer public key is required")
	}
	if options.Output == "" {
		return options, errors.New("output path is required")
	}
	if options.Duration == 0 {
		options.Duration = DefaultDuration
	}
	if options.Interval == 0 {
		options.Interval = DefaultInterval
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.Duration <= 0 || options.Duration > maxDuration {
		return options, fmt.Errorf("duration must be between 1ns and %s", maxDuration)
	}
	if options.Interval < minInterval || options.Interval > maxInterval {
		return options, fmt.Errorf("interval must be between %s and %s", minInterval, maxInterval)
	}
	if options.MaxBytes < minMaxBytes || options.MaxBytes > maxMaxBytes {
		return options, fmt.Errorf("max bytes must be between %d and %d", minMaxBytes, maxMaxBytes)
	}
	return options, nil
}

func (c *collector) open() error {
	var err error
	if c.statusFile, err = openSecureOutputFile(c.options.Output, "status.ndjson"); err != nil {
		return err
	}
	if c.eventFile, err = openSecureOutputFile(c.options.Output, "controller-events.ndjson"); err != nil {
		return err
	}
	if c.csvFile, err = openSecureOutputFile(c.options.Output, "peer-telemetry.csv"); err != nil {
		return err
	}
	c.csv = csv.NewWriter(c.budget.writer(c.csvFile))
	if err := c.csv.Write(csvHeader); err != nil {
		return err
	}
	c.csv.Flush()
	return c.csv.Error()
}

func (c *collector) close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	var result error
	if c.csv != nil {
		c.csv.Flush()
		result = errors.Join(result, c.csv.Error())
	}
	for _, file := range []*os.File{c.statusFile, c.eventFile, c.csvFile} {
		if file != nil {
			result = errors.Join(result, file.Sync(), file.Close())
		}
	}
	return result
}

func (c *collector) writeManifest() error {
	revision, modified := sourceRevision()
	return c.writeJSONFile("manifest.json", Manifest{
		SchemaVersion: 1, Interface: c.options.Interface,
		PeerPublicKey: c.options.PeerPublicKey, SessionID: c.options.SessionID,
		StartedAt: c.started, DurationMillis: c.options.Duration.Milliseconds(),
		IntervalMillis: c.options.Interval.Milliseconds(), MaxBytes: c.options.MaxBytes,
		Version: c.options.Version, SourceCommit: revision, SourceModified: modified,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(),
		Artifacts: []string{
			"status.ndjson", "peer-telemetry.csv", "controller-events.ndjson", "summary.json",
		},
		RequiredCapabilities: []string{
			"session_telemetry_v1", "recent_session_telemetry_v1", "session_events_v1",
		},
		Redaction: "no configuration, secret keys, tokens, or application payloads are collected",
	})
}

func sourceRevision() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return revision, modified
}

func (c *collector) writeJSONFile(name string, value any) error {
	file, err := openSecureOutputFile(c.options.Output, name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		data = append(data, '\n')
		_, err = c.budget.writer(file).Write(data)
	}
	return errors.Join(err, file.Sync(), file.Close())
}

func (c *collector) bootstrap(ctx context.Context, status management.Status) error {
	if err := requireCapabilities(status.Capabilities); err != nil {
		return err
	}
	if status.SupervisorEpoch == "" {
		return boundaryError("runtime status has no supervisor epoch")
	}
	c.epoch = status.SupervisorEpoch
	return c.drainEvents(ctx, false)
}

func requireCapabilities(capabilities []string) error {
	available := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		available[capability] = true
	}
	for _, required := range []string{
		"session_telemetry_v1", "recent_session_telemetry_v1", "session_events_v1",
	} {
		if !available[required] {
			return fmt.Errorf("runtime does not support %s", required)
		}
	}
	return nil
}

func (c *collector) sample(ctx context.Context) error {
	status, err := c.client.Status(ctx, c.options.Interface)
	c.samples++
	if err != nil {
		c.failedSamples++
		if writeErr := c.writeRawStatus(nil, err); writeErr != nil {
			return writeErr
		}
		if writeErr := c.writeFailedCSV(management.Status{SupervisorEpoch: c.epoch}, err); writeErr != nil {
			return writeErr
		}
		if eventErr := c.writeEventError(err); eventErr != nil {
			return eventErr
		}
		return fmt.Errorf("sample %d status: %w", c.samples, err)
	}
	if err := c.writeRawStatus(&status, nil); err != nil {
		return err
	}
	if status.SupervisorEpoch != c.epoch {
		return boundaryError(
			"supervisor epoch changed from %s to %s", c.epoch, status.SupervisorEpoch,
		)
	}
	selection, selectionErr := selectSession(status, c.options.PeerPublicKey, c.options.SessionID)
	if selectionErr != nil {
		c.failedSamples++
		if err := c.consumePendingFinal(status); err != nil {
			selectionErr = errors.Join(selectionErr, err)
		}
		if err := c.writeFailedCSV(status, selectionErr); err != nil {
			return err
		}
		if err := c.drainEvents(ctx, true); err != nil {
			return err
		}
		return fmt.Errorf("sample %d: %w", c.samples, selectionErr)
	}
	if err := c.transition(status, selection); err != nil {
		c.failedSamples++
		if writeErr := c.writeFailedCSV(status, err); writeErr != nil {
			return writeErr
		}
		if eventErr := c.drainEvents(ctx, true); eventErr != nil {
			return eventErr
		}
		return fmt.Errorf("sample %d: %w", c.samples, err)
	}
	if eventErr := c.drainEvents(ctx, true); eventErr != nil {
		c.failedSamples++
		if writeErr := c.writeFailedCSV(status, eventErr); writeErr != nil {
			return writeErr
		}
		return eventErr
	}
	return nil
}

func (c *collector) consumePendingFinal(status management.Status) error {
	if c.current == nil || c.finalized[c.current.key] {
		return nil
	}
	final, ok := findFinalSession(status.RecentSessions, c.current.key, c.options.PeerPublicKey)
	if !ok {
		return fmt.Errorf(
			"active target disappeared without retained final snapshot (evicted total %d)",
			status.RecentSessionsEvicted,
		)
	}
	if err := c.writeFinalCSV(status, final); err != nil {
		return err
	}
	c.finalized[c.current.key] = true
	return nil
}

func selectSession(
	status management.Status,
	peerPublicKey string,
	explicitSessionID uint64,
) (selectedSession, error) {
	candidates := make([]selectedSession, 0, 2)
	for _, session := range status.Sessions {
		if explicitSessionID != 0 && session.SessionID != explicitSessionID {
			continue
		}
		var authenticated, configured bool
		for _, peer := range session.Peers {
			if peer.PublicKey == peerPublicKey {
				authenticated = authenticated || peer.Authenticated
				configured = configured || peer.Configured
			}
		}
		if !authenticated && !configured {
			continue
		}
		candidates = append(candidates, selectedSession{
			key: seriesKey{
				Epoch: status.SupervisorEpoch, SessionID: session.SessionID,
				Generation: session.SessionGeneration,
			},
			role: session.Role, state: session.State,
			configuredEndpoint: session.ConfiguredEndpoint,
			currentEndpoint:    session.CurrentEndpoint,
			authenticated:      authenticated, configured: configured,
			stats: session.Stats,
		})
	}
	if len(candidates) == 0 {
		message := fmt.Sprintf("target peer %s has no active attributed session", peerPublicKey)
		if explicitSessionID != 0 {
			message = fmt.Sprintf("target peer %s has no active attributed session %d", peerPublicKey, explicitSessionID)
		}
		if status.SessionTelemetryOmitted != 0 {
			message += fmt.Sprintf(" (%d active sessions omitted)", status.SessionTelemetryOmitted)
		}
		return selectedSession{}, errors.New(message)
	}
	authenticated := candidates[:0]
	for _, candidate := range candidates {
		if candidate.authenticated {
			authenticated = append(authenticated, candidate)
		}
	}
	if len(authenticated) == 1 {
		return authenticated[0], nil
	}
	if len(authenticated) > 1 {
		return selectedSession{}, fmt.Errorf(
			"target peer %s is authenticated on %d active sessions; use --session-id",
			peerPublicKey, len(authenticated),
		)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return selectedSession{}, fmt.Errorf(
		"target peer %s is configured on %d active sessions; use --session-id",
		peerPublicKey, len(candidates),
	)
}

func (c *collector) transition(status management.Status, next selectedSession) error {
	if c.current == nil {
		c.current = &next
		c.targetSeries[next.key] = true
		c.havePrevious = false
		return c.writeSessionCSV(status, next, false, false, telemetry.SessionStats{})
	}
	if c.current.key == next.key {
		delta, err := counterDelta(c.previousStats, next.stats)
		if err != nil && c.havePrevious {
			return err
		}
		valid := c.havePrevious
		if err := c.writeSessionCSV(status, next, false, valid, delta); err != nil {
			return err
		}
		c.previousStats = next.stats
		c.havePrevious = true
		return nil
	}
	if !c.finalized[c.current.key] {
		final, ok := findFinalSession(status.RecentSessions, c.current.key, c.options.PeerPublicKey)
		if !ok {
			return fmt.Errorf(
				"session changed from %d/%d to %d/%d without retained final snapshot (evicted total %d)",
				c.current.key.SessionID, c.current.key.Generation,
				next.key.SessionID, next.key.Generation, status.RecentSessionsEvicted,
			)
		}
		if err := c.writeFinalCSV(status, final); err != nil {
			return err
		}
		c.finalized[c.current.key] = true
	}
	c.current = &next
	c.targetSeries[next.key] = true
	c.havePrevious = false
	return c.writeSessionCSV(status, next, false, false, telemetry.SessionStats{})
}

func findFinalSession(
	recent []telemetry.ClosedSessionObservation,
	key seriesKey,
	peerPublicKey string,
) (telemetry.ClosedSessionObservation, bool) {
	for _, session := range recent {
		if session.SessionID != key.SessionID || session.SessionGeneration != key.Generation {
			continue
		}
		for _, peer := range session.Peers {
			if peer.PublicKey == peerPublicKey {
				return session, true
			}
		}
	}
	return telemetry.ClosedSessionObservation{}, false
}

func (c *collector) writeFinalCSV(
	status management.Status,
	final telemetry.ClosedSessionObservation,
) error {
	selected := selectedSession{
		key: seriesKey{
			Epoch: c.epoch, SessionID: final.SessionID,
			Generation: final.SessionGeneration,
		},
		role: final.Role, state: final.State,
		configuredEndpoint: final.ConfiguredEndpoint,
		currentEndpoint:    final.CurrentEndpoint,
		stats:              final.FinalStats,
	}
	for _, peer := range final.Peers {
		if peer.PublicKey == c.options.PeerPublicKey {
			selected.authenticated = peer.Authenticated
			selected.configured = peer.Configured
		}
	}
	delta, err := counterDelta(c.previousStats, selected.stats)
	if err != nil && c.havePrevious {
		return fmt.Errorf("final session counters: %w", err)
	}
	return c.writeSessionCSV(status, selected, true, c.havePrevious, delta)
}

func (c *collector) writeRawStatus(status *management.Status, sampleErr error) error {
	record := rawStatusSample{
		SampleSequence: c.samples, SampledAt: time.Now(), Status: status,
	}
	if sampleErr != nil {
		record.Error = sampleErr.Error()
	}
	return writeJSONLine(c.budget.writer(c.statusFile), record)
}

func (c *collector) writeEventError(eventErr error) error {
	return writeJSONLine(c.budget.writer(c.eventFile), map[string]any{
		"supervisor_epoch": c.epoch, "sample_sequence": c.samples,
		"wall_time": time.Now(), "error": eventErr.Error(),
	})
}

func (c *collector) writeFailedCSV(status management.Status, sampleErr error) error {
	row := make([]string, len(csvHeader))
	setCSV(row, "sample_sequence", strconv.FormatUint(c.samples, 10))
	setCSV(row, "sample_state", "failed")
	setCSV(row, "error", sampleErr.Error())
	setCSV(row, "sampled_at", time.Now().Format(time.RFC3339Nano))
	setCSV(row, "monotonic_elapsed_ns", strconv.FormatInt(time.Since(c.started).Nanoseconds(), 10))
	setCSV(row, "event_monotonic_elapsed_ns", c.eventMonotonicElapsed())
	setCSV(row, "supervisor_epoch", status.SupervisorEpoch)
	setCSV(row, "event_stream_id", c.eventStreamID)
	setCSV(row, "peer_public_key", c.options.PeerPublicKey)
	setCSV(row, "session_id", strconv.FormatUint(c.options.SessionID, 10))
	return c.writeCSV(row)
}

func (c *collector) writeSessionCSV(
	status management.Status,
	session selectedSession,
	final bool,
	deltaValid bool,
	delta telemetry.SessionStats,
) error {
	statsJSON, err := json.Marshal(session.stats)
	if err != nil {
		return err
	}
	var deltaJSON []byte
	if deltaValid {
		deltaJSON, err = json.Marshal(delta)
		if err != nil {
			return err
		}
	}
	row := make([]string, len(csvHeader))
	values := map[string]string{
		"sample_sequence": strconv.FormatUint(c.samples, 10),
		"sample_state":    "ok", "sampled_at": time.Now().Format(time.RFC3339Nano),
		"monotonic_elapsed_ns":       strconv.FormatInt(time.Since(c.started).Nanoseconds(), 10),
		"event_monotonic_elapsed_ns": c.eventMonotonicElapsed(),
		"supervisor_epoch":           status.SupervisorEpoch, "event_stream_id": c.eventStreamID,
		"peer_public_key":    c.options.PeerPublicKey,
		"session_id":         strconv.FormatUint(session.key.SessionID, 10),
		"session_generation": strconv.FormatUint(session.key.Generation, 10),
		"role":               session.role, "session_state": session.state,
		"configured_endpoint": session.configuredEndpoint,
		"current_endpoint":    session.currentEndpoint,
		"authenticated":       strconv.FormatBool(session.authenticated),
		"configured":          strconv.FormatBool(session.configured),
		"final":               strconv.FormatBool(final), "delta_valid": strconv.FormatBool(deltaValid),
		"wg_tx_bytes": u64(session.stats.WGTxBytes), "wg_tx_bytes_delta": deltaValue(deltaValid, delta.WGTxBytes),
		"wg_rx_bytes": u64(session.stats.WGRxBytes), "wg_rx_bytes_delta": deltaValue(deltaValid, delta.WGRxBytes),
		"wire_tx_bytes": u64(session.stats.WireTxBytes), "wire_tx_bytes_delta": deltaValue(deltaValid, delta.WireTxBytes),
		"wire_rx_bytes": u64(session.stats.WireRxBytes), "wire_rx_bytes_delta": deltaValue(deltaValid, delta.WireRxBytes),
		"quic_packets_acked": u64(session.stats.QUICPacketsAcked), "quic_packets_acked_delta": deltaValue(deltaValid, delta.QUICPacketsAcked),
		"quic_packets_lost": u64(session.stats.QUICPacketsLost), "quic_packets_lost_delta": deltaValue(deltaValid, delta.QUICPacketsLost),
		"quic_spurious_loss_packets": u64(session.stats.QUICSpuriousLossPackets), "quic_spurious_loss_packets_delta": deltaValue(deltaValid, delta.QUICSpuriousLossPackets),
		"quic_pto_count": u64(session.stats.QUICPTOCount), "quic_pto_count_delta": deltaValue(deltaValid, delta.QUICPTOCount),
		"queue_drops": u64(session.stats.QueueDrops), "queue_drops_delta": deltaValue(deltaValid, delta.QueueDrops),
		"quic_datagram_rcv_queue_len":         u64(session.stats.QUICDatagramRcvQueueLen),
		"quic_datagram_rcv_queue_drops":       u64(session.stats.QUICDatagramRcvQueueDrops),
		"quic_datagram_rcv_queue_drops_delta": deltaValue(deltaValid, delta.QUICDatagramRcvQueueDrops),
		"quic_datagram_rcv_queue_high_water":  u64(session.stats.QUICDatagramRcvQueueHighWater),
		"fec_raw_lost":                        u64(session.stats.FECRawLost), "fec_raw_lost_delta": deltaValue(deltaValid, delta.FECRawLost),
		"fec_recovered": u64(session.stats.FECRecovered), "fec_recovered_delta": deltaValue(deltaValid, delta.FECRecovered),
		"fec_unrecovered": u64(session.stats.FECUnrecovered), "fec_unrecovered_delta": deltaValue(deltaValid, delta.FECUnrecovered),
		"quic_cwnd_bytes":              u64(session.stats.QUICCongestionWindowBytes),
		"quic_bytes_in_flight":         u64(session.stats.QUICBytesInFlight),
		"quic_bandwidth_estimate_bps":  u64(session.stats.QUICBandwidthEstimateBps),
		"quic_pacing_rate_bps":         u64(session.stats.QUICPacingRateBps),
		"quic_smoothed_rtt_us":         u64(session.stats.QUICSmoothedRTTUs),
		"quic_path_rtt_us":             u64(session.stats.QUICPathRTTUs),
		"quic_queue_delay_us":          u64(session.stats.QUICQueueDelayUs),
		"quic_congestion_model_state":  u64(session.stats.QUICCongestionModelState),
		"kernel_rx_overflow_supported": strconv.FormatBool(status.Stats.ReceiveQueueOverflow.Supported),
		"kernel_rx_overflow_source":    status.Stats.ReceiveQueueOverflow.Source,
		"kernel_rx_overflow_platform":  status.Stats.ReceiveQueueOverflow.Platform,
		"kernel_rx_overflow_packets":   u64(status.Stats.ReceiveQueueOverflow.Packets),
		"stats_json":                   string(statsJSON), "delta_json": string(deltaJSON),
	}
	if status.Stats.ReceiveQueueOverflow.Supported {
		if c.haveOverflow && status.Stats.ReceiveQueueOverflow.Packets >= c.overflow {
			values["kernel_rx_overflow_packets_delta"] = u64(status.Stats.ReceiveQueueOverflow.Packets - c.overflow)
		}
		c.overflow = status.Stats.ReceiveQueueOverflow.Packets
		c.haveOverflow = true
	}
	for key, value := range values {
		setCSV(row, key, value)
	}
	if err := c.writeCSV(row); err != nil {
		return err
	}
	c.previousStats = session.stats
	c.havePrevious = true
	return nil
}

func (c *collector) writeCSV(row []string) error {
	if err := c.csv.Write(row); err != nil {
		return err
	}
	c.csv.Flush()
	return c.csv.Error()
}

func (c *collector) drainEvents(ctx context.Context, write bool) error {
	for page := 0; page < maxEventPages; page++ {
		batch, err := c.client.Events(
			ctx, c.options.Interface, c.eventStreamID, c.eventCursor, eventPageSize,
		)
		if err != nil {
			if write {
				_ = c.writeEventError(err)
			}
			return fmt.Errorf("read controller events: %w", err)
		}
		if batch.SupervisorEpoch != c.epoch {
			return boundaryError(
				"event supervisor epoch changed from %s to %s", c.epoch, batch.SupervisorEpoch,
			)
		}
		if c.eventStreamID == "" {
			if batch.EventStreamID == "" {
				return boundaryError("controller event response has no stream ID")
			}
			c.eventStreamID = batch.EventStreamID
		} else if batch.EventStreamID != c.eventStreamID {
			return boundaryError(
				"controller event stream changed from %s to %s", c.eventStreamID, batch.EventStreamID,
			)
		}
		if batch.MonotonicElapsedNS >= 0 {
			c.eventClockOffsetNS = batch.MonotonicElapsedNS - time.Since(c.started).Nanoseconds()
			c.haveEventClock = true
		}
		if c.eventCursor != 0 && batch.FirstAvailableSequence > c.eventCursor+1 {
			return boundaryError(
				"controller event gap after %d; first available is %d",
				c.eventCursor, batch.FirstAvailableSequence,
			)
		}
		if len(batch.Events) == 0 {
			return nil
		}
		for _, event := range batch.Events {
			if event.EventSequence <= c.eventCursor {
				continue
			}
			if write && (event.SessionID == 0 || c.targetSeries[seriesKey{
				Epoch: c.epoch, SessionID: event.SessionID, Generation: event.SessionGeneration,
			}]) {
				if err := writeJSONLine(c.budget.writer(c.eventFile), collectedEvent{
					SupervisorEpoch: c.epoch, SessionEvent: event,
				}); err != nil {
					return err
				}
				c.events++
			}
			c.eventCursor = event.EventSequence
		}
		if len(batch.Events) < eventPageSize {
			return nil
		}
	}
	return boundaryError("controller event stream did not quiesce after %d pages", maxEventPages)
}

func (c *collector) eventMonotonicElapsed() string {
	if !c.haveEventClock {
		return ""
	}
	value := c.eventClockOffsetNS + time.Since(c.started).Nanoseconds()
	if value < 0 {
		value = 0
	}
	return strconv.FormatInt(value, 10)
}

func (c *collector) recordIssue(err error) {
	if len(c.issues) >= 8 {
		return
	}
	c.issues = append(c.issues, err.Error())
}

func counterDelta(previous, current telemetry.SessionStats) (telemetry.SessionStats, error) {
	var delta telemetry.SessionStats
	counters := []struct {
		name string
		old  uint64
		new  uint64
		set  func(uint64)
	}{
		{"wg_tx_packets", previous.WGTxPackets, current.WGTxPackets, func(v uint64) { delta.WGTxPackets = v }},
		{"wg_tx_bytes", previous.WGTxBytes, current.WGTxBytes, func(v uint64) { delta.WGTxBytes = v }},
		{"wg_rx_packets", previous.WGRxPackets, current.WGRxPackets, func(v uint64) { delta.WGRxPackets = v }},
		{"wg_rx_bytes", previous.WGRxBytes, current.WGRxBytes, func(v uint64) { delta.WGRxBytes = v }},
		{"wire_tx_packets", previous.WireTxPackets, current.WireTxPackets, func(v uint64) { delta.WireTxPackets = v }},
		{"wire_tx_bytes", previous.WireTxBytes, current.WireTxBytes, func(v uint64) { delta.WireTxBytes = v }},
		{"wire_rx_packets", previous.WireRxPackets, current.WireRxPackets, func(v uint64) { delta.WireRxPackets = v }},
		{"wire_rx_bytes", previous.WireRxBytes, current.WireRxBytes, func(v uint64) { delta.WireRxBytes = v }},
		{"queue_drops", previous.QueueDrops, current.QueueDrops, func(v uint64) { delta.QueueDrops = v }},
		{"fec_data_tx", previous.FECDataTx, current.FECDataTx, func(v uint64) { delta.FECDataTx = v }},
		{"fec_parity_tx", previous.FECParityTx, current.FECParityTx, func(v uint64) { delta.FECParityTx = v }},
		{"fec_raw_lost", previous.FECRawLost, current.FECRawLost, func(v uint64) { delta.FECRawLost = v }},
		{"fec_recovered", previous.FECRecovered, current.FECRecovered, func(v uint64) { delta.FECRecovered = v }},
		{"fec_unrecovered", previous.FECUnrecovered, current.FECUnrecovered, func(v uint64) { delta.FECUnrecovered = v }},
		{"quic_bytes_sent", previous.QUICBytesSent, current.QUICBytesSent, func(v uint64) { delta.QUICBytesSent = v }},
		{"quic_packets_sent", previous.QUICPacketsSent, current.QUICPacketsSent, func(v uint64) { delta.QUICPacketsSent = v }},
		{"quic_bytes_received", previous.QUICBytesReceived, current.QUICBytesReceived, func(v uint64) { delta.QUICBytesReceived = v }},
		{"quic_packets_received", previous.QUICPacketsReceived, current.QUICPacketsReceived, func(v uint64) { delta.QUICPacketsReceived = v }},
		{"quic_bytes_acked", previous.QUICBytesAcked, current.QUICBytesAcked, func(v uint64) { delta.QUICBytesAcked = v }},
		{"quic_packets_acked", previous.QUICPacketsAcked, current.QUICPacketsAcked, func(v uint64) { delta.QUICPacketsAcked = v }},
		{"quic_bytes_lost", previous.QUICBytesLost, current.QUICBytesLost, func(v uint64) { delta.QUICBytesLost = v }},
		{"quic_packets_lost", previous.QUICPacketsLost, current.QUICPacketsLost, func(v uint64) { delta.QUICPacketsLost = v }},
		{"quic_spurious_loss_packets", previous.QUICSpuriousLossPackets, current.QUICSpuriousLossPackets, func(v uint64) { delta.QUICSpuriousLossPackets = v }},
		{"quic_pto_count", previous.QUICPTOCount, current.QUICPTOCount, func(v uint64) { delta.QUICPTOCount = v }},
		{"quic_datagram_rcv_queue_drops", previous.QUICDatagramRcvQueueDrops, current.QUICDatagramRcvQueueDrops, func(v uint64) { delta.QUICDatagramRcvQueueDrops = v }},
	}
	for _, counter := range counters {
		if counter.new < counter.old {
			return telemetry.SessionStats{}, fmt.Errorf(
				"counter %s moved backwards from %d to %d within one session generation",
				counter.name, counter.old, counter.new,
			)
		}
		counter.set(counter.new - counter.old)
	}
	return delta, nil
}

var csvHeader = []string{
	"sample_sequence", "sample_state", "error", "sampled_at", "monotonic_elapsed_ns",
	"event_monotonic_elapsed_ns",
	"supervisor_epoch", "event_stream_id", "peer_public_key", "session_id",
	"session_generation", "role", "session_state", "configured_endpoint",
	"current_endpoint", "authenticated", "configured", "final", "delta_valid",
	"wg_tx_bytes", "wg_tx_bytes_delta", "wg_rx_bytes", "wg_rx_bytes_delta",
	"wire_tx_bytes", "wire_tx_bytes_delta", "wire_rx_bytes", "wire_rx_bytes_delta",
	"quic_packets_acked", "quic_packets_acked_delta", "quic_packets_lost",
	"quic_packets_lost_delta", "quic_spurious_loss_packets",
	"quic_spurious_loss_packets_delta", "quic_pto_count", "quic_pto_count_delta",
	"queue_drops", "queue_drops_delta", "quic_datagram_rcv_queue_len",
	"quic_datagram_rcv_queue_drops", "quic_datagram_rcv_queue_drops_delta",
	"quic_datagram_rcv_queue_high_water", "fec_raw_lost", "fec_raw_lost_delta",
	"fec_recovered", "fec_recovered_delta", "fec_unrecovered", "fec_unrecovered_delta",
	"quic_cwnd_bytes", "quic_bytes_in_flight", "quic_bandwidth_estimate_bps",
	"quic_pacing_rate_bps", "quic_smoothed_rtt_us", "quic_path_rtt_us",
	"quic_queue_delay_us", "quic_congestion_model_state",
	"kernel_rx_overflow_supported", "kernel_rx_overflow_source",
	"kernel_rx_overflow_platform", "kernel_rx_overflow_packets",
	"kernel_rx_overflow_packets_delta", "stats_json", "delta_json",
}

var csvColumns = func() map[string]int {
	result := make(map[string]int, len(csvHeader))
	for index, name := range csvHeader {
		result[name] = index
	}
	return result
}()

func setCSV(row []string, key, value string) {
	row[csvColumns[key]] = value
}

func u64(value uint64) string { return strconv.FormatUint(value, 10) }

func deltaValue(valid bool, value uint64) string {
	if !valid {
		return ""
	}
	return u64(value)
}

func writeJSONLine(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = writer.Write(data)
	return err
}

var errOutputLimit = errors.New("observation output byte limit exceeded")

type byteBudget struct {
	mu      sync.Mutex
	limit   int64
	written int64
}

func (b *byteBudget) writer(writer io.Writer) io.Writer {
	return budgetWriter{budget: b, writer: writer}
}

func (b *byteBudget) writtenBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written
}

type budgetWriter struct {
	budget *byteBudget
	writer io.Writer
}

func (w budgetWriter) Write(data []byte) (int, error) {
	w.budget.mu.Lock()
	defer w.budget.mu.Unlock()
	if int64(len(data)) > w.budget.limit-w.budget.written {
		return 0, errOutputLimit
	}
	n, err := w.writer.Write(data)
	w.budget.written += int64(n)
	return n, err
}

type observationBoundaryError struct{ message string }

func (e observationBoundaryError) Error() string { return e.message }

func boundaryError(format string, args ...any) error {
	return observationBoundaryError{message: fmt.Sprintf(format, args...)}
}

func isBoundaryError(err error) bool {
	var boundary observationBoundaryError
	return errors.As(err, &boundary)
}
