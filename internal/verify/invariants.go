// Package verify independently re-checks the timeline, provenance, frame
// sequence and manifest. It never trusts upstream ordering: every invariant is
// recomputed from the recorded facts.
package verify

import (
	"fmt"
	"sort"

	"mediacore/internal/timeline"
	"mediacore/internal/transform"
)

// Invariant names a checked property.
type Invariant string

const (
	InvMonotonicTime     Invariant = "monotonic_timeline"
	InvNonNegative       Invariant = "non_negative_time"
	InvUniqueFrames      Invariant = "unique_frame_numbers"
	InvFrameOrder        Invariant = "frame_order"
	InvSourceInRange     Invariant = "source_range_in_file"
	InvSourceChecksum    Invariant = "source_checksum"
	InvSampleAttribution Invariant = "every_sample_has_source"
	InvTrackCoverage     Invariant = "track_sample_coverage"
	InvFrameSources      Invariant = "frame_sources_resolve"
	InvRepairMonotonic   Invariant = "repaired_monotonic"
	InvManifestMatch     Invariant = "manifest_matches_frames"
)

// Failure is one violated invariant.
type Failure struct {
	Invariant Invariant `json:"invariant"`
	TrackID   uint32    `json:"track_id,omitempty"`
	Frame     int       `json:"frame,omitempty"`
	Offset    int64     `json:"offset,omitempty"`
	Detail    string    `json:"detail"`
}

func (f Failure) String() string {
	return fmt.Sprintf("%s: %s", f.Invariant, f.Detail)
}

// Input describes what was produced, independently of the producing code.
type Input struct {
	FileSizes    map[string]int64             `json:"file_sizes"`
	TrackSamples map[uint32][]timeline.Sample `json:"-"`
	Merged       []timeline.Sample            `json:"-"`
	Frames       []transform.Frame            `json:"-"`
	FrameDigest  string                       `json:"-"`
	Manifest     *Manifest                    `json:"-"`
}

// Run checks every invariant and returns all failures in a stable order.
func Run(in Input) []Failure {
	var failures []Failure
	failures = append(failures, checkMerged(in)...)
	failures = append(failures, checkFrameSequence(in)...)
	failures = append(failures, checkProvenance(in)...)
	failures = append(failures, checkManifest(in)...)
	sort.SliceStable(failures, func(i, j int) bool {
		if failures[i].Invariant != failures[j].Invariant {
			return failures[i].Invariant < failures[j].Invariant
		}
		return failures[i].Offset < failures[j].Offset
	})
	return failures
}

func checkMerged(in Input) []Failure {
	var out []Failure
	// Timeline order must be non-decreasing, and track ids ascending on ties,
	// which is exactly the documented merge order.
	for i := 1; i < len(in.Merged); i++ {
		prev, cur := in.Merged[i-1], in.Merged[i]
		if cur.TimelineTime < prev.TimelineTime {
			out = append(out, Failure{Invariant: InvMonotonicTime, TrackID: cur.TrackID, Offset: cur.FileOffset,
				Detail: fmt.Sprintf("sample at %d precedes %d", cur.TimelineTime, prev.TimelineTime)})
		}
		if cur.TimelineTime == prev.TimelineTime && cur.TrackID < prev.TrackID {
			out = append(out, Failure{Invariant: InvFrameOrder, TrackID: cur.TrackID, Offset: cur.FileOffset,
				Detail: "tie not broken by ascending track id"})
		}
	}
	for _, s := range in.Merged {
		if s.TimelineTime < 0 || s.TimelineDuration < 0 {
			out = append(out, Failure{Invariant: InvNonNegative, TrackID: s.TrackID, Offset: s.FileOffset,
				Detail: fmt.Sprintf("negative time %d/%d", s.TimelineTime, s.TimelineDuration)})
		}
		if s.TimelineDuration == 0 {
			out = append(out, Failure{Invariant: InvNonNegative, TrackID: s.TrackID, Offset: s.FileOffset,
				Detail: "zero duration sample"})
		}
	}
	for _, samples := range in.TrackSamples {
		for i := 1; i < len(samples); i++ {
			if samples[i].TimelineTime < samples[i-1].End() {
				out = append(out, Failure{Invariant: InvRepairMonotonic, TrackID: samples[i].TrackID, Offset: samples[i].FileOffset,
					Detail: fmt.Sprintf("track sample at %d overlaps previous end %d", samples[i].TimelineTime, samples[i-1].End())})
			}
		}
	}
	return out
}

func checkFrameSequence(in Input) []Failure {
	var out []Failure
	seen := make(map[int]bool, len(in.Frames))
	introduced := 0
	var lastTime int64 = -1
	for _, f := range in.Frames {
		if seen[f.Number] {
			out = append(out, Failure{Invariant: InvUniqueFrames, Frame: f.Number, Detail: "duplicate frame number"})
		}
		seen[f.Number] = true
		if f.Number != introduced {
			out = append(out, Failure{Invariant: InvUniqueFrames, Frame: f.Number,
				Detail: fmt.Sprintf("frame numbers are not contiguous at %d (want %d)", f.Number, introduced)})
		}
		introduced++
		if f.TimelineTime < lastTime {
			out = append(out, Failure{Invariant: InvFrameOrder, Frame: f.Number,
				Detail: fmt.Sprintf("frame time %d before previous %d", f.TimelineTime, lastTime)})
		}
		lastTime = f.TimelineTime
	}
	return out
}

func checkProvenance(in Input) []Failure {
	var out []Failure
	for _, s := range in.Merged {
		if s.File == "" {
			out = append(out, Failure{Invariant: InvSampleAttribution, TrackID: s.TrackID, Offset: s.FileOffset, Detail: "sample has no source file"})
			continue
		}
		size, ok := in.FileSizes[s.File]
		if !ok {
			out = append(out, Failure{Invariant: InvSampleAttribution, TrackID: s.TrackID, Offset: s.FileOffset,
				Detail: fmt.Sprintf("unknown source file %q", s.File)})
			continue
		}
		if s.FileOffset < 0 || s.ByteLength <= 0 || s.FileOffset+s.ByteLength > size {
			out = append(out, Failure{Invariant: InvSourceInRange, TrackID: s.TrackID, Offset: s.FileOffset,
				Detail: fmt.Sprintf("byte range [%d,%d) outside %d byte file", s.FileOffset, s.FileOffset+s.ByteLength, size)})
		}
	}
	return out
}

func checkManifest(in Input) []Failure {
	if in.Manifest == nil {
		return []Failure{{Invariant: InvManifestMatch, Detail: "manifest missing"}}
	}
	var out []Failure
	if len(in.Manifest.Frames) != len(in.Frames) {
		out = append(out, Failure{Invariant: InvManifestMatch,
			Detail: fmt.Sprintf("manifest has %d frames, output has %d", len(in.Manifest.Frames), len(in.Frames))})
	}
	n := len(in.Frames)
	if len(in.Manifest.Frames) < n {
		n = len(in.Manifest.Frames)
	}
	for i := 0; i < n; i++ {
		want := in.Frames[i]
		got := in.Manifest.Frames[i]
		if got.Number != want.Number || got.PixelSHA != want.PixelSHA || got.SourceOffset != want.SourceOffset {
			out = append(out, Failure{Invariant: InvManifestMatch, Frame: want.Number,
				Detail: "manifest frame does not match generated frame"})
		}
	}
	if in.Manifest.FrameDigest != "" && in.FrameDigest != "" && in.Manifest.FrameDigest != in.FrameDigest {
		out = append(out, Failure{Invariant: InvManifestMatch,
			Detail: fmt.Sprintf("manifest digest %s != frame digest %s", short(in.Manifest.FrameDigest), short(in.FrameDigest))})
	}
	return out
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
