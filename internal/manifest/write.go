// Package manifest renders the final JSON manifest. Every number is an
// estimate derived from sample payload bytes and track metadata; the
// document says so explicitly and is deterministic so its SHA-256 can be
// used to prove that a rerun made the same decisions.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"mediamerge/internal/audio"
	"mediamerge/internal/fingerprint"
	"mediamerge/internal/ingest"
	"mediamerge/internal/model"
	"mediamerge/internal/segment"
	"mediamerge/internal/track"
)

// Schema is the manifest format identifier.
const Schema = "mediamerge.manifest.v1"

// Note is printed in every manifest so no metric is mistaken for a
// measurement of decoded media.
const Note = "All bitrate, silence and quality metrics are estimates derived " +
	"from sample payload bytes and track metadata. No audio is resampled, " +
	"decoded or transcoded, and no video is transcoded; resampling is only " +
	"planned as a rational ratio."

// Bitrate carries the target/actual estimate.
type Bitrate struct {
	TargetBps    int64   `json:"target_bitrate_bps"`
	ActualBps    int64   `json:"actual_bitrate_bps"`
	DeviationPct float64 `json:"bitrate_deviation_pct"`
	Estimated    bool    `json:"estimated"`
	Basis        string  `json:"basis"`
}

// Counts are the headline counts for one run or the suite.
type Counts struct {
	Segments       int   `json:"segments"`
	SilenceMs      int64 `json:"silence_ms"`
	Duplicates     int   `json:"duplicates"`
	Collisions     int   `json:"collisions"`
	QualitySamples int64 `json:"quality_samples"`
}

// SegmentRecord explains how one output segment was formed.
type SegmentRecord struct {
	Index           int                  `json:"index"`
	StartNs         int64                `json:"start_ns"`
	EndNs           int64                `json:"end_ns"`
	DurationMs      float64              `json:"duration_ms"`
	RequestedEndNs  int64                `json:"requested_end_ns"`
	BoundaryShiftMs float64              `json:"boundary_shift_ms"`
	SampleCount     int                  `json:"sample_count"`
	Bytes           int64                `json:"bytes"`
	Tracks          []uint32             `json:"tracks"`
	Sources         []segment.SourceFrag `json:"sources"`
}

// TrackRecord is the normalized metadata plus per-track analysis.
type TrackRecord struct {
	ID               uint32                `json:"id"`
	Handler          string                `json:"handler"`
	Source           string                `json:"source"`
	Timescale        uint32                `json:"timescale"`
	SampleRate       uint32                `json:"sample_rate"`
	TargetSampleRate uint32                `json:"target_sample_rate,omitempty"`
	Channels         uint16                `json:"channels,omitempty"`
	Layout           string                `json:"layout,omitempty"`
	Mixable          bool                  `json:"mixable"`
	Color            *ingest.ColorMeta     `json:"color,omitempty"`
	ColorOverrides   []track.ColorOverride `json:"color_overrides,omitempty"`
	ChannelPlan      *track.ChannelPlan    `json:"channel_plan,omitempty"`
	Resample         *track.ResamplePlan   `json:"resample,omitempty"`
	Conflicts        []string              `json:"conflicts,omitempty"`
	SilenceMs        int64                 `json:"silence_ms"`
	MissingMs        int64                 `json:"missing_ms"`
	QualityParts     int64                 `json:"quality_samples"`
	PeakDBFS         *float64              `json:"peak_dbfs,omitempty"`
	RMSDBFS          *float64              `json:"rms_dbfs,omitempty"`
}

// PathRecord is one acceptance path (normal, damaged, recovered).
type PathRecord struct {
	Name             string                  `json:"name"`
	ExitCode         int                     `json:"exit_code"`
	Counts           Counts                  `json:"counts"`
	AlignmentErrorMs float64                 `json:"manifest_alignment_error_ms"`
	Bitrate          Bitrate                 `json:"bitrate"`
	RecoveredTracks  []uint32                `json:"recovered_tracks"`
	IsolatedTracks   []uint32                `json:"isolated_tracks"`
	Segments         []SegmentRecord         `json:"segments"`
	Gaps             []model.Gap             `json:"gaps"`
	Tracks           []TrackRecord           `json:"tracks"`
	Duplicates       []fingerprint.Duplicate `json:"duplicates,omitempty"`
	Collisions       []fingerprint.Collision `json:"collisions,omitempty"`
	CapsHit          []string                `json:"caps_hit,omitempty"`
	Diagnostics      []*model.Error          `json:"diagnostics"`
}

// RunInput is the data one pipeline run hands to the manifest.
type RunInput struct {
	Name         string
	ExitCode     int
	Segments     segment.Result
	Gaps         []model.Gap
	Audio        []audio.Report
	Fingerprints fingerprint.Report
	Tracks       *track.Normalized
	Errors       []*model.Error
	Recovered    []uint32
	Isolated     []uint32
}

// Input is the full suite input.
type Input struct {
	Seed      int64
	SegmentMs int
	TargetBps int64
	Runs      []RunInput
}

// Document is the manifest file.
type Document struct {
	Schema              string       `json:"schema"`
	Note                string       `json:"note"`
	Seed                int64        `json:"seed"`
	SegmentMs           int          `json:"segment_ms"`
	TargetBitrateBps    int64        `json:"target_bitrate_bps"`
	ActualBitrateBps    int64        `json:"actual_bitrate_bps"`
	BitrateDeviationPct float64      `json:"bitrate_deviation_pct"`
	BitrateEstimated    bool         `json:"bitrate_estimated"`
	Segments            int          `json:"segments"`
	SilenceMs           int64        `json:"silence_ms"`
	Duplicates          int          `json:"duplicates"`
	Collisions          int          `json:"collisions"`
	QualitySamples      int64        `json:"quality_samples"`
	AlignmentErrorMs    float64      `json:"manifest_alignment_error_ms"`
	RecoveredTracks     []uint32     `json:"recovered_tracks"`
	IsolatedTracks      []uint32     `json:"isolated_tracks"`
	Paths               []PathRecord `json:"paths"`
	ManifestSHA256      string       `json:"manifest_sha256,omitempty"`
}

// Build renders a deterministic document. No timestamps or map iteration
// order influence the output.
func Build(in Input) *Document {
	doc := &Document{Schema: Schema, Note: Note, Seed: in.Seed,
		SegmentMs: in.SegmentMs, TargetBitrateBps: in.TargetBps,
		BitrateEstimated: true}
	recSet := make(map[uint32]bool)
	isoSet := make(map[uint32]bool)
	var totalBytes, totalNs int64
	maxShift := model.NsDuration(0)
	for _, run := range in.Runs {
		pr := buildPath(run, in.TargetBps)
		doc.Paths = append(doc.Paths, pr)
		doc.Segments += pr.Counts.Segments
		doc.SilenceMs += pr.Counts.SilenceMs
		doc.Duplicates += pr.Counts.Duplicates
		doc.Collisions += pr.Counts.Collisions
		doc.QualitySamples += pr.Counts.QualitySamples
		for _, id := range pr.RecoveredTracks {
			recSet[id] = true
		}
		for _, id := range pr.IsolatedTracks {
			isoSet[id] = true
		}
		for _, s := range run.Segments.Segments {
			for _, smp := range s.Samples {
				totalBytes += int64(smp.Size)
			}
			totalNs += int64(s.Time.Len())
		}
		if run.Segments.MaxShiftNs > maxShift {
			maxShift = run.Segments.MaxShiftNs
		}
	}
	doc.RecoveredTracks = sortedKeys(recSet)
	doc.IsolatedTracks = sortedKeys(isoSet)
	doc.AlignmentErrorMs = round2(float64(maxShift) / 1e6)
	doc.ActualBitrateBps, doc.BitrateDeviationPct = bitrate(totalBytes, totalNs, in.TargetBps)
	if h, err := doc.Hash(); err == nil {
		doc.ManifestSHA256 = h
	}
	return doc
}

func buildPath(run RunInput, target int64) PathRecord {
	pr := PathRecord{Name: run.Name, ExitCode: run.ExitCode,
		CapsHit:         run.Segments.CapsHit,
		RecoveredTracks: append([]uint32(nil), run.Recovered...),
		IsolatedTracks:  append([]uint32(nil), run.Isolated...),
		Gaps:            run.Gaps,
		Duplicates:      run.Fingerprints.Duplicates,
		Collisions:      run.Fingerprints.Collisions,
		Diagnostics:     run.Errors}
	for _, seg := range run.Segments.Segments {
		rec := SegmentRecord{
			Index: seg.Index, StartNs: int64(seg.Time.Start), EndNs: int64(seg.Time.End),
			DurationMs:      round2(float64(seg.Time.Len()) / 1e6),
			RequestedEndNs:  int64(seg.RequestedEndNs),
			BoundaryShiftMs: round2(float64(seg.BoundaryShift) / 1e6),
			SampleCount:     len(seg.Samples),
			Tracks:          seg.Tracks,
			Sources:         seg.Sources,
		}
		for _, s := range seg.Samples {
			rec.Bytes += int64(s.Size)
		}
		pr.Segments = append(pr.Segments, rec)
	}
	pr.AlignmentErrorMs = round2(float64(run.Segments.MaxShiftNs) / 1e6)
	pr.Counts.Segments = len(run.Segments.Segments)
	pr.Counts.Duplicates = len(run.Fingerprints.Duplicates)
	pr.Counts.Collisions = len(run.Fingerprints.Collisions)
	for _, rep := range run.Audio {
		pr.Counts.SilenceMs += rep.SilentMs()
		pr.Counts.QualitySamples += rep.Stats.PCMParts
	}
	var bytes, ns int64
	for _, rec := range pr.Segments {
		bytes += rec.Bytes
		ns += rec.EndNs - rec.StartNs
	}
	pr.Bitrate = Bitrate{TargetBps: target, Estimated: true,
		Basis: "sum of sample payload bytes over segment duration"}
	pr.Bitrate.ActualBps, pr.Bitrate.DeviationPct = bitrate(bytes, ns, target)
	pr.Tracks = buildTracks(run)
	return pr
}

func buildTracks(run RunInput) []TrackRecord {
	if run.Tracks == nil {
		return nil
	}
	byID := make(map[uint32]audio.Report)
	for _, rep := range run.Audio {
		byID[rep.TrackID] = rep
	}
	out := make([]TrackRecord, 0, len(run.Tracks.Tracks))
	for _, md := range run.Tracks.Tracks {
		tr := TrackRecord{
			ID: md.ID, Handler: md.Handler, Source: md.Source,
			Timescale: md.Timescale, SampleRate: md.SampleRate,
			TargetSampleRate: md.TargetSampleRate, Channels: md.Channels,
			Layout: md.Layout, Mixable: md.Mixable, Color: md.Color,
			ColorOverrides: md.ColorOverrides, ChannelPlan: md.ChannelPlan,
			Resample: md.Resample, Conflicts: md.Conflicts,
		}
		if rep, ok := byID[md.ID]; ok {
			tr.SilenceMs = rep.SilentMs()
			tr.QualityParts = rep.Stats.PCMParts
			for _, iv := range rep.Missing {
				tr.MissingMs += int64(iv.Len()) / 1_000_000
			}
			peak, rms := rep.Stats.PeakDBFS, rep.Stats.RMSDBFS
			tr.PeakDBFS, tr.RMSDBFS = &peak, &rms
		}
		out = append(out, tr)
	}
	return out
}

func bitrate(bytes, ns, target int64) (int64, float64) {
	if ns <= 0 {
		return 0, 0
	}
	actual := int64(math.Round(float64(bytes) * 8 * 1e9 / float64(ns)))
	if target <= 0 {
		return actual, 0
	}
	dev := round2((float64(actual) - float64(target)) / float64(target) * 100)
	return actual, dev
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func sortedKeys(set map[uint32]bool) []uint32 {
	out := make([]uint32, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if out == nil {
		out = []uint32{}
	}
	return out
}

// Hash returns the SHA-256 of the canonical document with the hash field
// omitted. It is stable across runs for identical input.
func (d *Document) Hash() (string, error) {
	clone := *d
	clone.ManifestSHA256 = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Write hashes the document, stores the hash and writes indented JSON to a
// local path. It returns the manifest hash.
func (d *Document) Write(path string) (string, error) {
	hash, err := d.Hash()
	if err != nil {
		return "", err
	}
	d.ManifestSHA256 = hash
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", fmt.Errorf("manifest: write %s: %w", path, err)
	}
	return hash, nil
}
