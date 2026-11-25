// Package report aggregates the quantitative output of every pipeline stage
// into one deterministic summary: packet recovery counters, jitter levels,
// alignment drift, render hashes and pixel/audio statistics. The summary is
// serialized to stable JSON and can be recomputed and compared by verify.
package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"

	"mediasync/internal/render"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
)

// PacketSummary is the recovery-stage portion of the report.
type PacketSummary struct {
	TotalPackets int         `json:"total_packets"`
	Corrupt      int         `json:"corrupt_packets"`
	Duplicates   int         `json:"duplicates"`
	Late         int         `json:"late_packets"`
	Missing      int         `json:"missing_packets"`
	Emitted      int         `json:"emitted_packets"`
	HighWater    int         `json:"buffer_high_water"`
	Gaps         []rtp.Gap   `json:"gaps"`
	JitterTarget int         `json:"jitter_target"`
	JitterMin    int         `json:"jitter_min_level"`
	JitterMax    int         `json:"jitter_max_level"`
	SmoothedBPS  int64       `json:"smoothed_bps"`
}

// FrameSummary is one rendered frame's quantitative record.
type FrameSummary struct {
	Index      int     `json:"index"`
	SHA256     string  `json:"sha256"`
	PixelMin   uint8   `json:"pixel_min"`
	PixelMax   uint8   `json:"pixel_max"`
	PixelMean  float64 `json:"pixel_mean"`
	PixelStd   float64 `json:"pixel_std"`
	StartTick  int64   `json:"start_tick"`
	AlignError int64   `json:"align_error_tick"`
}

// AudioSummary is one rendered audio fragment's quantitative record.
type AudioSummary struct {
	Index         int     `json:"index"`
	SHA256        string  `json:"sha256"`
	PeakAmplitude int16   `json:"peak_amplitude"`
	PeakLoudness  float64 `json:"peak_loudness_dbfs"`
	StartTick     int64   `json:"start_tick"`
	AlignError    int64   `json:"align_error_tick"`
}

// AlignmentSummary captures the measured A/V drift.
type AlignmentSummary struct {
	MaxDriftTick int64 `json:"max_drift_tick"`
	MaxDriftMs   int64 `json:"max_drift_ms"`
	DriftSamples int   `json:"drift_samples"`
	CueCount     int   `json:"cue_count"`
}

// Summary is the full quantitative report. Field order and map handling are
// fixed so the JSON encoding is byte-stable for identical inputs.
type Summary struct {
	Schema    int              `json:"schema"`
	Seed      int64            `json:"seed"`
	Packets   PacketSummary    `json:"packets"`
	Alignment AlignmentSummary `json:"alignment"`
	Frames    []FrameSummary   `json:"frames"`
	Audio     []AudioSummary   `json:"audio"`
}

// Build assembles a Summary from the stage outputs. Slices are copied so the
// report cannot be mutated by later pipeline activity.
func Build(seed int64, stats rtp.RecoveryStats, align *sync.Result,
	frames []render.Frame, audio []render.AudioFragment) *Summary {
	s := &Summary{
		Schema: 1,
		Seed:   seed,
		Packets: PacketSummary{
			TotalPackets: stats.TotalPackets, Corrupt: stats.Corrupt,
			Duplicates: stats.Duplicates, Late: stats.Late, Missing: stats.Missing,
			Emitted: stats.Emitted, HighWater: stats.HighWater,
			Gaps: append([]rtp.Gap(nil), stats.Gaps...),
			JitterTarget: stats.JitterTarget, JitterMin: stats.JitterMin,
			JitterMax: stats.JitterMax, SmoothedBPS: stats.SmoothedBPS,
		},
	}
	if align != nil {
		s.Alignment = AlignmentSummary{
			MaxDriftTick: align.MaxDriftTick, MaxDriftMs: align.MaxDriftMs,
			DriftSamples: len(align.Drifts), CueCount: len(align.Cues),
		}
	}
	for _, f := range frames {
		s.Frames = append(s.Frames, FrameSummary{
			Index: f.Index, SHA256: f.SHA256, PixelMin: f.PixelMin, PixelMax: f.PixelMax,
			PixelMean: f.PixelMean, PixelStd: f.PixelStd, StartTick: f.StartTick,
			AlignError: f.AlignError,
		})
	}
	for _, a := range audio {
		s.Audio = append(s.Audio, AudioSummary{
			Index: a.Index, SHA256: a.SHA256, PeakAmplitude: a.PeakAmplitude,
			PeakLoudness: a.PeakLoudness, StartTick: a.StartTick, AlignError: a.AlignError,
		})
	}
	return s
}

// MarshalJSON emits stable, indented JSON with HTML escaping disabled.
func (s *Summary) MarshalJSON() ([]byte, error) {
	// A distinct alias type breaks the MarshalJSON recursion: encoding the
	// alias uses default struct reflection rather than this method.
	type plain Summary
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode((*plain)(s)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Hash returns a SHA-256 over the stable JSON encoding, so two runs with
// identical summaries produce identical hashes.
func (s *Summary) Hash() (string, error) {
	data, err := s.MarshalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Compare reports the first field that differs between two summaries. It
// returns nil when they are equal, enabling verify to compare item by item.
func Compare(a, b *Summary) error {
	ha, err := a.Hash()
	if err != nil {
		return err
	}
	hb, err := b.Hash()
	if err != nil {
		return err
	}
	if ha == hb {
		return nil
	}
	// Hashes differ: find a human-readable first difference.
	if !reflect.DeepEqual(a.Packets, b.Packets) {
		return &Mismatch{Field: "packets", Detail: "recovery counters differ"}
	}
	if a.Alignment != b.Alignment {
		return &Mismatch{Field: "alignment", Detail: "drift summary differs"}
	}
	if n := mismatchFrames(a.Frames, b.Frames); n != "" {
		return &Mismatch{Field: "frames", Detail: n}
	}
	if n := mismatchAudio(a.Audio, b.Audio); n != "" {
		return &Mismatch{Field: "audio", Detail: n}
	}
	return &Mismatch{Field: "summary", Detail: "hash differs but no scalar field matched a case"}
}

func mismatchFrames(a, b []FrameSummary) string {
	if len(a) != len(b) {
		return "frame count differs"
	}
	for i := range a {
		if a[i] != b[i] {
			return "frame " + strconv.Itoa(a[i].Index) + " differs"
		}
	}
	return ""
}

func mismatchAudio(a, b []AudioSummary) string {
	if len(a) != len(b) {
		return "audio fragment count differs"
	}
	for i := range a {
		if a[i] != b[i] {
			return "audio fragment " + strconv.Itoa(a[i].Index) + " differs"
		}
	}
	return ""
}

// Mismatch describes one summary comparison failure.
type Mismatch struct {
	Field  string
	Detail string
}

func (m *Mismatch) Error() string { return "report: " + m.Field + ": " + m.Detail }

// SortedHashes returns the frame and audio hashes in index order, which for
// deterministic render input is already sorted; the function enforces it so
// callers never depend on render iteration order.
func SortedHashes(frames []FrameSummary, audio []AudioSummary) (frameHash, audioHash []string) {
	fs := append([]FrameSummary(nil), frames...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Index < fs[j].Index })
	for _, x := range fs {
		frameHash = append(frameHash, x.SHA256)
	}
	a := append([]AudioSummary(nil), audio...)
	sort.Slice(a, func(i, j int) bool { return a[i].Index < a[j].Index })
	for _, x := range a {
		audioHash = append(audioHash, x.SHA256)
	}
	return frameHash, audioHash
}
