package fec

import (
	"encoding/binary"
	"encoding/json"
	"math/rand/v2"
	"os"
	"sort"
	"testing"
	"time"

	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

// Opt-in packet-erasure experiment. It uses the actual encoder and decoder,
// but deliberately excludes TCP, QUIC pacing and OS queues: its output is
// recovery/overhead evidence, never an application goodput prediction.
func TestAdaptiveInterleaveExperiment(t *testing.T) {
	if os.Getenv("WG_QUIC_FEC_EXPERIMENT") != "1" {
		t.Skip("set WG_QUIC_FEC_EXPERIMENT=1 to run the recovery experiment")
	}
	type result struct {
		Model           string  `json:"model"`
		FPS             int     `json:"source_frames_per_second"`
		Interleave      int     `json:"interleave"`
		FlushMS         int     `json:"flush_ms"`
		Frames          int     `json:"source_frames"`
		Lost            int     `json:"source_frames_lost"`
		Unrecovered     int     `json:"source_frames_unrecovered"`
		MeanK           float64 `json:"mean_k"`
		WireRatio       float64 `json:"wire_source_ratio"`
		ResidualPercent float64 `json:"residual_percent"`
	}
	var results []result
	for _, model := range []string{"independent", "burst8"} {
		for _, fps := range []int{500, 2000, 5000} {
			for _, setting := range []struct{ interleave, flush int }{{1, 2}, {4, 2}, {4, 8}} {
				const frames = 20000
				controller := NewController()
				controller.setParity(4)
				controller.interleave = setting.interleave
				controller.interleaveSnapshot.Store(int32(setting.interleave))
				encoder := NewEncoder(32, controller)
				decoder := NewDecoder()
				rng := rand.New(rand.NewPCG(7, 9))
				bad := false
				drop := func() bool {
					if model == "independent" {
						return rng.Float64() < .02
					}
					if bad {
						if rng.Float64() < 1.0/8 {
							bad = false
						}
					} else if rng.Float64() < .02/.98/8 {
						bad = true
					}
					return bad
				}
				seen := make([]bool, frames)
				delivered, lost, wireBytes, groups := 0, 0, 0, 0
				emit := func(now time.Time, packets [][]byte) {
					for _, raw := range packets {
						p, _, err := parsePacket(raw)
						if err != nil {
							t.Fatal(err)
						}
						wireBytes += len(raw)
						if p.kind == KindClose {
							groups++
						}
						if drop() {
							if p.kind == KindData {
								lost++
							}
						} else {
							out, err := decoder.Handle(now, raw)
							if err != nil {
								t.Fatal(err)
							}
							for _, frame := range out.Frames {
								id := binary.LittleEndian.Uint64(frame[:8])
								if id >= frames {
									t.Fatalf("corrupted source ID %d", id)
								}
								if !seen[id] {
									seen[id] = true
									delivered++
								}
							}
						}
						quiccarrier.ReleaseDatagramSendBuffer(raw)
					}
				}
				now := time.Unix(1, 0)
				interval := time.Second / time.Duration(fps)
				deadline := time.Duration(setting.flush) * time.Millisecond
				var flushAt time.Time
				frame := make([]byte, 1300)
				for id := range frames {
					now = time.Unix(1, 0).Add(time.Duration(id) * interval)
					if !flushAt.IsZero() && !now.Before(flushAt) {
						packets, err := encoder.Flush()
						if err != nil {
							t.Fatal(err)
						}
						emit(flushAt, packets)
						flushAt = time.Time{}
					}
					binary.LittleEndian.PutUint64(frame[:8], uint64(id))
					packets, err := encoder.Add(frame)
					if err != nil {
						t.Fatal(err)
					}
					emit(now, packets)
					if !encoder.Pending() {
						flushAt = time.Time{}
					} else if flushAt.IsZero() {
						flushAt = now.Add(deadline)
					}
				}
				packets, err := encoder.Flush()
				if err != nil {
					t.Fatal(err)
				}
				emit(now.Add(deadline), packets)
				results = append(results, result{
					model, fps, setting.interleave, setting.flush, frames, lost, frames - delivered,
					float64(frames) / float64(groups), float64(wireBytes) / float64(frames*1300), 100 * float64(frames-delivered) / frames,
				})
			}
		}
	}
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("results:\n%s", raw)
}

// Run this identical fixture against two source revisions to compare policies.
// Each invocation runs only the compiled revision's controller. FlushDelay's
// presence selects that revision's send-loop timing; it is not a policy
// override. Older revisions used the base delay and retained an active timer
// when interleave changed. No TCP goodput can be inferred from these results.
// Feedback is delivered after 10ms, sorted by group ID within each expiration
// batch for reproducibility. Both revisions consume the same packet-indexed
// loss tape for each seed; losses are not chosen to favor recovered sources.
func TestAdaptiveControllerExperiment(t *testing.T) {
	if os.Getenv("WG_QUIC_FEC_EXPERIMENT") != "1" {
		t.Skip("opt-in experiment")
	}
	type result struct {
		Model              string  `json:"model"`
		FPS                int     `json:"fps"`
		Seed               int     `json:"seed"`
		AdaptiveWindow     bool    `json:"adaptive_window"`
		Lost               int     `json:"source_lost"`
		ResidualPercent    float64 `json:"residual_pct"`
		WireRatio          float64 `json:"wire_ratio"`
		InterleavedPercent float64 `json:"interleaved_pct"`
	}
	var results []result
	for _, model := range []string{"independent", "burst8"} {
		for _, fps := range []int{500, 2000, 5000} {
			for seed := range 10 {
				rng := rand.New(rand.NewPCG(uint64(seed+7), 9))
				drops := make([]bool, 250000)
				bad := false
				for i := range drops {
					if model == "independent" {
						drops[i] = rng.Float64() < .02
						continue
					}
					if bad {
						if rng.Float64() < 1.0/8 {
							bad = false
						}
					} else if rng.Float64() < .02/.98/8 {
						bad = true
					}
					drops[i] = bad
				}
				const frames = 20000
				c := NewController()
				e := NewEncoder(32, c)
				clocked, adaptiveWindow := any(e).(interface {
					FlushDelay(time.Duration) time.Duration
				})
				d := NewDecoder()
				e.ObservePathRTT(20 * time.Millisecond)
				e.ObserveTransport(0, 0)
				var sent, transportLost uint64
				wire, lost, delivered, interleaved := 0, 0, 0, 0
				seen := make([]bool, frames)
				type pending struct {
					at       time.Time
					feedback Feedback
				}
				var feedbacks []pending
				queueFeedback := func(now time.Time, list []Feedback) {
					sort.Slice(list, func(i, j int) bool { return list[i].GroupID < list[j].GroupID })
					for _, feedback := range list {
						feedbacks = append(feedbacks, pending{now.Add(10 * time.Millisecond), feedback})
					}
				}
				observe := func(now time.Time) {
					for len(feedbacks) > 0 && !now.Before(feedbacks[0].at) {
						feedback := feedbacks[0].feedback
						feedbacks = feedbacks[1:]
						if uint32(feedback.Epoch) != e.epochSnapshot.Load() {
							continue
						}
						e.Observe(feedback)
					}
				}
				emit := func(now time.Time, packets [][]byte) {
					for _, raw := range packets {
						kind, fecPacket := PacketKind(raw)
						wire += len(raw)
						if drops[sent] {
							transportLost++
							if !fecPacket || kind == KindData {
								lost++
							}
						} else {
							out, err := d.Handle(now, raw)
							if err != nil {
								t.Fatal(err)
							}
							queueFeedback(now, out.SendFeedback)
							decoded := out.Frames
							if !out.Handled {
								decoded = [][]byte{raw}
							}
							for _, frame := range decoded {
								id := binary.LittleEndian.Uint64(frame[:8])
								if id >= frames {
									t.Fatal("corrupt output")
								}
								if !seen[id] {
									seen[id] = true
									delivered++
								}
							}
						}
						sent++
						quiccarrier.ReleaseDatagramSendBuffer(raw)
					}
				}
				interval := time.Second / time.Duration(fps)
				base := 2 * time.Millisecond
				var flushAt time.Time
				frame := make([]byte, 1300)
				now := time.Unix(1, 0)
				for id := range frames {
					now = time.Unix(1, 0).Add(time.Duration(id) * interval)
					if !flushAt.IsZero() && !now.Before(flushAt) {
						packets, err := e.Flush()
						if err != nil {
							t.Fatal(err)
						}
						emit(flushAt, packets)
						flushAt = time.Time{}
					}
					observe(now)
					if id%32 == 0 {
						e.ObserveTransport(sent, transportLost)
					}
					if c.CurrentInterleave() > 1 {
						interleaved++
					}
					binary.LittleEndian.PutUint64(frame[:8], uint64(id))
					previousInterleave := e.interleave
					packets, err := e.Add(frame)
					if err != nil {
						t.Fatal(err)
					}
					emit(now, packets)
					if !e.Pending() {
						flushAt = time.Time{}
					} else if flushAt.IsZero() || (adaptiveWindow && e.interleave != previousInterleave) {
						deadline := base
						if adaptiveWindow {
							deadline = clocked.FlushDelay(base)
						}
						flushAt = now.Add(deadline)
					}
				}
				packets, err := e.Flush()
				if err != nil {
					t.Fatal(err)
				}
				emit(now.Add(8*time.Millisecond), packets)
				results = append(results, result{model, fps, seed, adaptiveWindow, lost, 100 * float64(frames-delivered) / frames, float64(wire) / (frames * 1300), 100 * float64(interleaved) / frames})
			}
		}
	}
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("results:\n%s", raw)
}
