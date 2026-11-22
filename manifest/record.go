// Package manifest turns per-track processing results into a verifiable,
// reproducible summary: aligned interval records, cross-stage invariants and
// an ISO BMFF (go-mp4) carrier that can be parsed back.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"medpipe/timeline"
)

// TrackRecord is the manifest entry for one processed track.
type TrackRecord struct {
	TrackID           string  `json:"track_id"`
	InputRate         uint32  `json:"input_rate_hz"`
	OutputRate        uint32  `json:"output_rate_hz"`
	InputFrames       int64   `json:"input_frames"`
	ResampledFrames   int64   `json:"resampled_frames"`
	OutputFrames      int64   `json:"output_frames"`
	StartSample       int64   `json:"start_sample"`
	EndSample         int64   `json:"end_sample"`
	DurationNanos     int64   `json:"duration_nanos"`
	InputLUFS         float64 `json:"input_lufs"`
	OutputLUFS        float64 `json:"output_lufs"`
	TargetLUFS        float64 `json:"target_lufs"`
	TruePeakDBTP      float64 `json:"true_peak_dbtp"`
	ClippedSamples    int64   `json:"quality_clipped_samples"`
	TargetBitrateBPS  int64   `json:"target_bitrate_bps"`
	ActualBitrateBPS  int64   `json:"actual_bitrate_bps"`
	BitrateDeltaPct   float64 `json:"bitrate_delta_pct"`
	AlignmentMillis   int64   `json:"manifest_alignment_ms"`
}

// FailureRecord locates a processing failure for the error manifest.
type FailureRecord struct {
	Code     timeline.ErrorCode `json:"code"`
	Stage    string             `json:"error_stage"`
	TrackID  string             `json:"track_id"`
	Message  string             `json:"message"`
	Checkpoint timeline.Checkpoint `json:"recovery_checkpoint"`
}

// Manifest is the full summary written to disk and embedded in the MP4 box.
type Manifest struct {
	Seed       int64          `json:"seed"`
	OutputRate uint32         `json:"output_rate_hz"`
	Channels   int            `json:"channels"`
	QueueCap   int            `json:"queue_capacity"`
	PeakQueue  int            `json:"peak_queue_blocks"`
	OutputSHA  string         `json:"output_sha256"`
	Tracks     []TrackRecord  `json:"tracks"`
	Failures   []FailureRecord `json:"failures,omitempty"`
}

// Builder accumulates records and runs the cross-stage invariants.
type Builder struct {
	clock    timeline.Clock
	manifest Manifest
}

// NewBuilder starts a manifest bound to the shared output clock.
func NewBuilder(clock timeline.Clock, seed int64, outRate uint32, channels, queueCap int) *Builder {
	return &Builder{
		clock:    clock,
		manifest: Manifest{Seed: seed, OutputRate: outRate, Channels: channels, QueueCap: queueCap},
	}
}

// AddTrack validates one record against the frame/duration invariants before
// storing it. A violating record is rejected rather than silently written.
func (b *Builder) AddTrack(r TrackRecord) error {
	if r.OutputFrames != r.EndSample-r.StartSample {
		return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, r.TrackID,
			"output frames %d do not cover interval [%d,%d)",
			r.OutputFrames, r.StartSample, r.EndSample)
	}
	if r.ResampledFrames != r.OutputFrames {
		return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, r.TrackID,
			"resampled %d != output %d frames", r.ResampledFrames, r.OutputFrames)
	}
	want := b.clock.SamplesToNanos(r.OutputFrames)
	if r.DurationNanos != want {
		return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, r.TrackID,
			"duration %d ns disagrees with %d output samples (%d ns)", r.DurationNanos, r.OutputFrames, want)
	}
	// Bitrate invariant: measured bitrate must stay within the declared band.
	if r.TargetBitrateBPS > 0 {
		delta := r.BitrateDeltaPct
		if delta < 0 {
			delta = -delta
		}
		if delta > 2.0 {
			return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, r.TrackID,
				"bitrate delta %.3f%% exceeds 2%% band", delta)
		}
	}
	if r.TruePeakDBTP > -1.0 {
		return timeline.NewError(timeline.ErrCodeDurationMismatch, timeline.StageManifest, r.TrackID,
			"true peak %.2f dBTP above -1 dBTP ceiling", r.TruePeakDBTP)
	}
	b.manifest.Tracks = append(b.manifest.Tracks, r)
	return nil
}

// AddFailure records a located failure together with the last durable
// checkpoint so a repaired fixture can resume.
func (b *Builder) AddFailure(f FailureRecord) {
	b.manifest.Failures = append(b.manifest.Failures, f)
}

// SetQueueStats records the peak bounded queue depth.
func (b *Builder) SetQueueStats(peak int) { b.manifest.PeakQueue = peak }

// HashOutput produces a deterministic sha256 over the canonical JSON of all
// track records, so identical parameters and inputs hash identically.
func (b *Builder) HashOutput() string {
	tracks := append([]TrackRecord(nil), b.manifest.Tracks...)
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].TrackID < tracks[j].TrackID })
	canonical, _ := json.Marshal(tracks)
	sum := sha256.Sum256(canonical)
	b.manifest.OutputSHA = hex.EncodeToString(sum[:])
	return b.manifest.OutputSHA
}

// Build freezes and returns the manifest with stable track ordering.
func (b *Builder) Build() Manifest {
	sort.Slice(b.manifest.Tracks, func(i, j int) bool {
		return b.manifest.Tracks[i].TrackID < b.manifest.Tracks[j].TrackID
	})
	b.HashOutput()
	return b.manifest
}

// MarshalJSON renders the manifest deterministically.
func (m Manifest) MarshalJSON() ([]byte, error) {
	type alias Manifest
	return json.MarshalIndent(alias(m), "", "  ")
}
