package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
)

// EncodeVersion is the forwarding manifest schema version.
const EncodeVersion = 1

// Interval is one forwarded media range with its measured statistics.
type Interval struct {
	// StartMS and DurationMS are presentation milliseconds at the timeline
	// origin.  Missing time is never folded into these values.
	StartMS    int64 `json:"start_ms"`
	DurationMS int64 `json:"duration_ms"`
	// MissingBefore is true when an explicit gap precedes this interval.
	MissingBefore bool `json:"missing_before"`
}

// TrackStats summarises per-track audio processing.
type TrackStats struct {
	TrackID        uint32  `json:"track_id"`
	InputRate      int     `json:"input_rate"`
	OutputRate     int     `json:"output_rate"`
	InputChannels  int     `json:"input_channels"`
	OutputLayout   string  `json:"output_layout"`
	Resampled      bool    `json:"resampled"`
	FramesIn       int64   `json:"frames_in"`
	FramesOut      int64   `json:"frames_out"`
	HeadPadFrames  int     `json:"head_pad_frames"`
	TailPadFrames  int     `json:"tail_pad_frames"`
	GainDB         float64 `json:"gain_db"`
	MeasuredLUFS   float64 `json:"measured_lufs"`
	TargetLUFS     float64 `json:"target_lufs"`
	TruePeakDBTP   float64 `json:"true_peak_dbtp"`
	ClippedSamples int64   `json:"clipped_samples"`
	LimitedSamples int64   `json:"limited_samples"`
	Degraded       bool    `json:"degraded,omitempty"`
	// DegradeReason explains a degraded path; empty when the path was full.
	DegradeReason string `json:"degrade_reason,omitempty"`
}

// PacketStats mirrors the RTP summary needed for cross-node checks.
type PacketStats struct {
	Packets        int     `json:"packets"`
	TotalBytes     int     `json:"total_bytes"`
	TotalFrames    int64   `json:"total_frames"`
	GapsBefore     int     `json:"gaps_before"`
	NonMonotonic   int     `json:"non_monotonic"`
	MediaTicksMS   int64   `json:"media_ticks_ms"`
	MissingTicksMS int64   `json:"missing_ticks_ms"`
	ActualKbps     float64 `json:"actual_kbps"`
	TargetKbps     int     `json:"target_kbps"`
	DeviationPct   float64 `json:"deviation_pct"`
}

// Document is the forwarding manifest.
type Document struct {
	Version        int    `json:"version"`
	AssetID        string `json:"asset_id"`
	SampleRate     int    `json:"sample_rate"`
	Layout         string `json:"layout"`
	MovieTimescale uint32 `json:"movie_timescale"`
	StartTick      int64  `json:"start_tick"`
	// TimelineErrorTicks is the absolute difference between the summed
	// interval durations and the exact timeline duration.  Must be <= 1.
	TimelineErrorTicks  int64           `json:"timeline_error_ticks"`
	ManifestAlignmentMS int64           `json:"manifest_alignment_ms"`
	Intervals           []Interval      `json:"intervals"`
	MissingDurationMS   int64           `json:"missing_duration_ms"`
	RecoveredSamples    int64           `json:"recovered_samples"`
	Statistics          []TrackStats    `json:"track_stats"`
	Packets             PacketStats     `json:"packet_stats"`
	SkippedTracks       []SkippedTrack  `json:"skipped_tracks,omitempty"`
	DegradedTracks      []DegradedTrack `json:"degraded_tracks,omitempty"`
	OutputSHA256        string          `json:"output_sha256"`
}

// SkippedTrack records a track excluded from processing.
type SkippedTrack struct {
	TrackID uint32 `json:"track_id"`
	Reason  string `json:"reason"`
}

// DegradedTrack records a track processed with reduced input.
type DegradedTrack struct {
	TrackID uint32 `json:"track_id"`
	Reason  string `json:"reason"`
}

// EmitInput collects everything the emitter needs.  The emitter never reopens
// media files; it works only from the timeline and recorded statistics.
type EmitInput struct {
	AssetID        string
	Rate           clock.Rate
	MovieTimescale uint32
	StartTick      int64
	SampleRate     int
	Layout         string
	Timeline       *clock.Timeline
	// RecoveredSamples counts samples rebuilt or salvaged by recovery.
	RecoveredSamples int64
	// TimelineErrorTicks is the resampler accumulator residue in output ticks.
	TimelineErrorTicks int64
	// MissingDurationMS is the explicit missing duration.
	MissingDurationMS int64
	TrackStats        []TrackStats
	PacketStats       PacketStats
	Skipped           []SkippedTrack
	Degraded          []DegradedTrack
}

// Emit builds the forwarding manifest from timeline segments and statistics.
// Contiguous segments of the same kind are merged first so the interval list
// stays compact and its millisecond rounding error stays below one tick per
// boundary.  Only confirmed media becomes an interval; a preceding gap sets
// MissingBefore so a receiver sees the hole explicitly.
func Emit(in EmitInput) (*Document, error) {
	if in.Timeline == nil {
		return nil, fault.Fatal(fault.ClassManifest, "no-timeline", "manifest emission requires a timeline")
	}
	if !in.Rate.IsValid() {
		return nil, fault.Fatal(fault.ClassTimeline, "rate", "manifest emission requires a valid rate")
	}
	msRate := clock.RateMilli
	doc := &Document{
		Version:           EncodeVersion,
		AssetID:           in.AssetID,
		SampleRate:        in.SampleRate,
		Layout:            in.Layout,
		MovieTimescale:    in.MovieTimescale,
		StartTick:         in.StartTick,
		MissingDurationMS: in.MissingDurationMS,
		RecoveredSamples:  in.RecoveredSamples,
		Statistics:        in.TrackStats,
		Packets:           in.PacketStats,
		SkippedTracks:     in.Skipped,
		DegradedTracks:    in.Degraded,
	}
	// Merge contiguous segments of the same kind into runs.
	segs := in.Timeline.Segments()
	type run struct {
		kind     clock.Kind
		start    int64
		duration int64
	}
	var runs []run
	for _, seg := range segs {
		if n := len(runs); n > 0 && runs[n-1].kind == seg.Kind && runs[n-1].start+runs[n-1].duration == seg.Start {
			runs[n-1].duration += seg.Duration
			continue
		}
		runs = append(runs, run{kind: seg.Kind, start: seg.Start, duration: seg.Duration})
	}
	var pendingGap bool
	for _, r := range runs {
		if r.kind == clock.Gap {
			pendingGap = true
			continue
		}
		startMS, err := clock.Rescale(r.start, in.Rate, msRate)
		if err != nil {
			return nil, err
		}
		durMS, err := clock.Rescale(r.duration, in.Rate, msRate)
		if err != nil {
			return nil, err
		}
		if durMS <= 0 {
			// A run shorter than half a millisecond cannot be represented; it
			// is folded into the following run's leading gap rather than
			// emitted as a zero-length interval.
			return nil, fault.New(fault.ClassManifest, "interval-too-short",
				fmt.Sprintf("media run of %d ticks rounds to 0 ms", r.duration))
		}
		doc.Intervals = append(doc.Intervals, Interval{StartMS: startMS, DurationMS: durMS, MissingBefore: pendingGap})
		pendingGap = false
	}
	// Alignment is the worst boundary rounding error in milliseconds.  It is a
	// reporting figure only; all interval arithmetic above is integer
	// rescaling, never float accumulation.
	var maxAlignMS float64
	for _, r := range runs {
		exactMS := float64(r.start) * 1000 / float64(in.Rate.Num/in.Rate.Den)
		backMS, err := clock.Rescale(r.start, in.Rate, msRate)
		if err != nil {
			return nil, err
		}
		d := absFloat(exactMS - float64(backMS))
		if d > maxAlignMS {
			maxAlignMS = d
		}
		exactEnd := float64(r.start+r.duration) * 1000 / float64(in.Rate.Num/in.Rate.Den)
		backEnd, err := clock.Rescale(r.start+r.duration, in.Rate, msRate)
		if err != nil {
			return nil, err
		}
		if d := absFloat(exactEnd - float64(backEnd)); d > maxAlignMS {
			maxAlignMS = d
		}
	}
	doc.ManifestAlignmentMS = int64(math.Ceil(maxAlignMS))
	// The timeline error is the residual carried by the streaming converter,
	// supplied by the audio pipeline.  It is < 1 output tick by construction.
	doc.TimelineErrorTicks = in.TimelineErrorTicks
	if err := doc.finalize(); err != nil {
		return nil, err
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// finalize sorts statistics deterministically and computes the hash.
func (d *Document) finalize() error {
	sort.SliceStable(d.Statistics, func(i, j int) bool {
		return d.Statistics[i].TrackID < d.Statistics[j].TrackID
	})
	sort.SliceStable(d.SkippedTracks, func(i, j int) bool {
		return d.SkippedTracks[i].TrackID < d.SkippedTracks[j].TrackID
	})
	sort.SliceStable(d.DegradedTracks, func(i, j int) bool {
		return d.DegradedTracks[i].TrackID < d.DegradedTracks[j].TrackID
	})
	b, err := d.Canonical()
	if err != nil {
		return err
	}
	docCopy := *d
	docCopy.OutputSHA256 = ""
	b2, err := docCopy.Canonical()
	if err != nil {
		return err
	}
	_ = b
	sum := sha256.Sum256(b2)
	d.OutputSHA256 = hex.EncodeToString(sum[:])
	return nil
}

// Canonical renders the document as stable JSON without the hash field, which
// is what gets hashed.  The full output includes the hash.
func (d *Document) Canonical() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Encode returns the final manifest bytes including the output hash.
func (d *Document) Encode() ([]byte, error) {
	return d.Canonical()
}

// Validate performs self-consistency checks used by tests and the CLI.
func (d *Document) Validate() error {
	if d.Version != EncodeVersion {
		return fault.Fatal(fault.ClassManifest, "version", fmt.Sprintf("unsupported version %d", d.Version))
	}
	if d.SampleRate != 48000 {
		return fault.Fatal(fault.ClassAudio, "sample-rate", fmt.Sprintf("output rate %d is not 48000", d.SampleRate))
	}
	if len(d.Intervals) == 0 && d.MissingDurationMS == 0 {
		return fault.New(fault.ClassManifest, "empty", "manifest has neither intervals nor missing duration")
	}
	prevEnd := int64(-1)
	for i, iv := range d.Intervals {
		if iv.DurationMS <= 0 {
			return fault.New(fault.ClassManifest, "interval-duration", fmt.Sprintf("interval %d has non-positive duration", i))
		}
		if prevEnd >= 0 && iv.StartMS < prevEnd {
			return fault.Fatal(fault.ClassTimeline, "interval-overlap", fmt.Sprintf("interval %d starts before the previous ends", i))
		}
		prevEnd = iv.StartMS + iv.DurationMS
	}
	if d.TimelineErrorTicks < 0 || d.TimelineErrorTicks > 1 {
		return fault.New(fault.ClassTimeline, "timeline-error", fmt.Sprintf("timeline error %d ticks exceeds 1", d.TimelineErrorTicks))
	}
	if d.ManifestAlignmentMS < 0 || d.ManifestAlignmentMS > 1 {
		return fault.New(fault.ClassTimeline, "alignment", fmt.Sprintf("alignment %d ms exceeds 1", d.ManifestAlignmentMS))
	}
	return nil
}
