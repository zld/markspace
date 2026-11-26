// Package manifest writes the recomputable evidence for a regression run:
// segment timeline, isolated gaps, PSI version history and quality metrics.
// Output is deterministic JSON so two runs over the same input diff cleanly.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"rtpregress/internal/audio"
	"rtpregress/internal/clock"
)

// SegmentEntry is one timeline row in the manifest.
type SegmentEntry struct {
	Segment     int    `json:"segment"`
	PID         string `json:"pid"`
	StartNanos  int64  `json:"start_nanos"`
	StartSample int64  `json:"start_sample"`
	Samples     int    `json:"samples"`
	PCMSHA256   string `json:"pcm_sha256"`
}

// GapEntry records an isolated interval with no trustworthy source audio.
type GapEntry struct {
	PID       string `json:"pid"`
	Offset    int64  `json:"packet_offset"`
	FromNanos int64  `json:"from_nanos"`
	ToNanos   int64  `json:"to_nanos"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
}

// PSIEntry records a PAT/PMT version transition.
type PSIEntry struct {
	Table      string `json:"table"`
	Offset     int64  `json:"packet_offset"`
	Version    uint8  `json:"version"`
	TableIDExt uint16 `json:"table_id_ext"`
}

// Report is the full manifest document.
type Report struct {
	Schema        string         `json:"schema"`
	SampleRate    int            `json:"sample_rate"`
	Channels      int            `json:"channels"`
	TargetBitrate int            `json:"bitrate_target"`
	TotalSamples  int64          `json:"total_samples"`
	DurationNanos int64          `json:"duration_nanos"`
	DriftPPM      int64          `json:"drift_ppm"`
	Segments      []SegmentEntry `json:"segments"`
	Gaps          []GapEntry     `json:"gaps"`
	PSIEvents     []PSIEntry     `json:"psi_events"`
	Metrics       audio.Metrics  `json:"metrics"`
	ManifestHash  string         `json:"manifest_hash"`
}

// Builder accumulates manifest rows and serializes them deterministically.
type Builder struct {
	rep Report
}

// NewBuilder starts a report for the canonical output specification.
func NewBuilder(sampleRate, channels, bitrate int) *Builder {
	return &Builder{rep: Report{
		Schema:        "rtp-regress/v1",
		SampleRate:    sampleRate,
		Channels:      channels,
		TargetBitrate: bitrate,
		Segments:      []SegmentEntry{},
		Gaps:          []GapEntry{},
		PSIEvents:     []PSIEntry{},
	}}
}

// AddSegment appends one merged segment row.
func (b *Builder) AddSegment(s SegmentEntry) { b.rep.Segments = append(b.rep.Segments, s) }

// AddGap appends one isolated gap row.
func (b *Builder) AddGap(g GapEntry) { b.rep.Gaps = append(b.rep.Gaps, g) }

// AddPSI appends one PSI version row.
func (b *Builder) AddPSI(p PSIEntry) { b.rep.PSIEvents = append(b.rep.PSIEvents, p) }

// SetTimeline records the global extent and measured clock drift.
func (b *Builder) SetTimeline(totalSamples, durationNanos, driftPPM int64) {
	b.rep.TotalSamples = totalSamples
	b.rep.DurationNanos = durationNanos
	b.rep.DriftPPM = driftPPM
}

// SetMetrics attaches the quality evidence.
func (b *Builder) SetMetrics(m audio.Metrics) { b.rep.Metrics = m }

// SetHash attaches the deterministic manifest hash from the merger.
func (b *Builder) SetHash(h string) { b.rep.ManifestHash = h }

// Write sorts every collection deterministically and writes pretty JSON.
func (b *Builder) Write(dir, name string) (string, error) {
	sort.SliceStable(b.rep.Segments, func(i, j int) bool {
		if b.rep.Segments[i].StartSample != b.rep.Segments[j].StartSample {
			return b.rep.Segments[i].StartSample < b.rep.Segments[j].StartSample
		}
		return b.rep.Segments[i].Segment < b.rep.Segments[j].Segment
	})
	sort.SliceStable(b.rep.Gaps, func(i, j int) bool {
		return b.rep.Gaps[i].FromNanos < b.rep.Gaps[j].FromNanos
	})
	sort.SliceStable(b.rep.PSIEvents, func(i, j int) bool {
		return b.rep.PSIEvents[i].Offset < b.rep.PSIEvents[j].Offset
	})
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", clock.New(clock.CodeManifest, "manifest", -1, 0, 0,
			"mkdir %s: %v", dir, err)
	}
	data, err := json.MarshalIndent(b.rep, "", "  ")
	if err != nil {
		return "", clock.New(clock.CodeManifest, "manifest", -1, 0, 0,
			"marshal: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", clock.New(clock.CodeManifest, "manifest", -1, 0, 0,
			"write %s: %v", path, err)
	}
	return path, nil
}

// HexPID renders a PID as the canonical 0x%04x string used across rows.
func HexPID(pid uint16) string { return fmt.Sprintf("0x%04x", pid) }
