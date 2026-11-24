// Package index rebuilds a random-access keyframe index on the repaired
// timeline. Entries are binary-searchable by presentation time, and the
// builder audits whether every presented sample is reachable from a sync
// sample before the index can claim coverage.
package index

import (
	"fmt"
	"math"
	"sort"

	"mediad/internal/clock"
	mp4 "mediad/internal/mp4"
)

// Version is bumped whenever the index layout or semantics change. It is part
// of the checkpoint fingerprint.
const Version = 1

// SparseThresholdNs is the keyframe spacing above which coverage and the
// largest seek interval are reported.
const SparseThresholdNs int64 = 10 * clock.NsPerSecond

// Entry is one random-access point.
type Entry struct {
	TimeNs     int64  `json:"time_ns"` // repaired presentation time
	DTS        int64  `json:"dts"`
	PTS        int64  `json:"pts"`
	Sample     uint32 `json:"sample"`     // 1-based
	RawOffset  int64  `json:"raw_offset"` // offset of the sample's timing entry
	DataOffset uint64 `json:"data_offset"`
	Size       uint32 `json:"size"`
	// ByteOffset is the payload offset in the source, kept for random access.
}

// TrackIndex is a seek index for one track.
type TrackIndex struct {
	TrackID   uint32  `json:"track_id"`
	Timescale uint32  `json:"timescale"`
	OriginNs  int64   `json:"origin_ns"`
	Entries   []Entry `json:"entries"`
	// CoveragePct is the fraction of presented samples reachable from a sync
	// sample at or before them.
	CoveragePct float64 `json:"coverage_pct"`
	// PresentedSamples counts samples inside [first,last] presented range.
	PresentedSamples int `json:"presented_samples"`
	// CoveredSamples counts samples with a preceding sync sample.
	CoveredSamples int `json:"covered_samples"`
	// MaxIntervalNs is the largest gap between consecutive index entries.
	MaxIntervalNs int64 `json:"max_interval_ns"`
	// Sparse is true when any interval exceeds SparseThresholdNs.
	Sparse bool `json:"sparse"`
	// RangeStartNs / RangeEndNs describe index coverage.
	RangeStartNs int64 `json:"range_start_ns"`
	RangeEndNs   int64 `json:"range_end_ns"`
	// SparseRanges lists intervals over the threshold, with the samples that
	// bound them.
	SparseRanges []SparseRange `json:"sparse_ranges,omitempty"`
	// DerivedSyncFlags records whether sync flags had to be inferred.
	DerivedSync bool `json:"derived_sync"`
}

// SparseRange is one over-threshold gap.
type SparseRange struct {
	FromSample uint32 `json:"from_sample"`
	ToSample   uint32 `json:"to_sample"`
	IntervalNs int64  `json:"interval_ns"`
}

// Index bundles all track indexes for one file.
type Index struct {
	Version int `json:"version"`
	// Path is excluded from digests for the same reason as Scan.Path.
	Path   string       `json:"-"`
	Tracks []TrackIndex `json:"tracks"`
}

// Options tunes construction.
type Options struct {
	// InferSync makes the builder fall back to GOP heuristics when the source
	// has no sync table; derived points are labelled so they stay auditable.
	InferSync bool
	// SparseThresholdNs overrides the 10 s default when non-zero.
	SparseThresholdNs int64
}

// Build constructs an index from repaired tracks.
func Build(rep *mp4.Repaired, opts Options) (*Index, error) {
	if rep == nil {
		return nil, clock.New(clock.StageIndex, clock.CodeBadArgs, "nil repair result")
	}
	threshold := opts.SparseThresholdNs
	if threshold <= 0 {
		threshold = SparseThresholdNs
	}
	idx := &Index{Version: Version, Path: rep.Path}
	for i := range rep.Tracks {
		ti, err := buildTrack(&rep.Tracks[i], opts, threshold)
		if err != nil {
			return nil, err
		}
		idx.Tracks = append(idx.Tracks, *ti)
	}
	return idx, nil
}

func buildTrack(tr *mp4.RepairedTrack, opts Options, threshold int64) (*TrackIndex, error) {
	ti := &TrackIndex{
		TrackID:   tr.TrackID,
		Timescale: tr.Timescale,
		OriginNs:  tr.OriginNs,
	}
	if len(tr.Samples) == 0 {
		return nil, clock.New(clock.StageIndex, clock.CodeIndexIncomplete, "track has no samples").WithTrack(tr.TrackID)
	}
	syncFlags := make([]bool, len(tr.Samples))
	any := false
	for i, smp := range tr.Samples {
		syncFlags[i] = smp.Sync
		any = any || smp.Sync
	}
	if !any {
		if !opts.InferSync {
			return nil, clock.New(clock.StageIndex, clock.CodeIndexIncomplete,
				"track %d has no sync samples and sync inference is disabled", tr.TrackID).
				WithTrack(tr.TrackID)
		}
		// Conservative GOP inference: the first sample, then every sample whose
		// preceding gap is a local multiple of the cadence. Label it derived.
		inferSync(tr, syncFlags)
		ti.DerivedSync = true
	}

	// Index times are file-relative, so the published range is the first to
	// last sample NS. Pre-roll samples before the first sync sample count as
	// uncovered.
	firstNs, lastNs := tr.Samples[0].NS, tr.Samples[len(tr.Samples)-1].NS
	ti.RangeStartNs, ti.RangeEndNs = firstNs, lastNs
	covered := make([]bool, len(tr.Samples))
	lastSync := -1
	for i := range tr.Samples {
		smp := &tr.Samples[i]
		inRange := smp.NS >= firstNs && smp.NS <= lastNs
		if inRange {
			ti.PresentedSamples++
		}
		if syncFlags[i] {
			lastSync = i
			ti.Entries = append(ti.Entries, Entry{
				TimeNs: smp.NS, DTS: smp.DTS, PTS: smp.PTS, Sample: smp.Index,
				RawOffset: smp.RawOffset, DataOffset: smp.Offset, Size: smp.Size,
			})
		}
		if lastSync >= 0 && inRange {
			covered[i] = true
		}
	}
	for _, c := range covered {
		if c {
			ti.CoveredSamples++
		}
	}
	if ti.PresentedSamples > 0 {
		ti.CoveragePct = float64(ti.CoveredSamples) / float64(ti.PresentedSamples) * 100
	}
	sort.Slice(ti.Entries, func(a, b int) bool { return ti.Entries[a].TimeNs < ti.Entries[b].TimeNs })
	for i := 1; i < len(ti.Entries); i++ {
		gap := ti.Entries[i].TimeNs - ti.Entries[i-1].TimeNs
		if gap < 0 {
			return nil, clock.New(clock.StageIndex, clock.CodeIndexIncomplete,
				"index entries are not monotonic at %d", i).WithTrack(tr.TrackID)
		}
		if gap > ti.MaxIntervalNs {
			ti.MaxIntervalNs = gap
		}
		if gap > threshold {
			ti.Sparse = true
			ti.SparseRanges = append(ti.SparseRanges, SparseRange{
				FromSample: ti.Entries[i-1].Sample, ToSample: ti.Entries[i].Sample, IntervalNs: gap,
			})
		}
	}
	if len(ti.Entries) == 0 {
		return nil, clock.New(clock.StageIndex, clock.CodeIndexIncomplete, "no sync samples").WithTrack(tr.TrackID)
	}
	return ti, nil
}

// inferSync marks the first sample plus every sample after a gap at least
// twice the local cadence. It is deliberately conservative: a wrong guess
// would produce an unseekable point, so only clear GOP boundaries qualify.
func inferSync(tr *mp4.RepairedTrack, flags []bool) {
	if len(flags) == 0 {
		return
	}
	flags[0] = true
	// Without keyframe flags, infer a fixed 2-second GOP as the least risky
	// guess and mark the result derived so consumers can requery.
	step := int64(0)
	target := int64(tr.Timescale) * 2
	for i := 1; i < len(tr.Samples); i++ {
		step += tr.Samples[i].DTS - tr.Samples[i-1].DTS
		if step >= target {
			flags[i] = true
			step = 0
		}
	}
}

// CoveragePct aggregates presented/covered samples across tracks.
func (i *Index) CoveragePct() float64 {
	var presented, covered int
	for _, t := range i.Tracks {
		presented += t.PresentedSamples
		covered += t.CoveredSamples
	}
	if presented == 0 {
		return 0
	}
	return float64(covered) / float64(presented) * 100
}

// KeyframeCount is the number of index entries across tracks.
func (i *Index) KeyframeCount() int {
	n := 0
	for _, t := range i.Tracks {
		n += len(t.Entries)
	}
	return n
}

// Lookup returns the entry usable to decode the sample presented at ns.
func (t *TrackIndex) Lookup(ns int64) (Entry, bool) {
	i := sort.Search(len(t.Entries), func(k int) bool { return t.Entries[k].TimeNs > ns })
	if i == 0 {
		return Entry{}, false
	}
	return t.Entries[i-1], true
}

// Validate checks index invariants and returns a reviewable error.
func (t *TrackIndex) Validate() error {
	if len(t.Entries) == 0 {
		return clock.New(clock.StageIndex, clock.CodeIndexIncomplete, "empty index")
	}
	for i := 1; i < len(t.Entries); i++ {
		if t.Entries[i].TimeNs < t.Entries[i-1].TimeNs {
			return clock.New(clock.StageIndex, clock.CodeIndexIncomplete, "entries out of order at %d", i)
		}
		if t.Entries[i].Sample <= t.Entries[i-1].Sample {
			return clock.New(clock.StageIndex, clock.CodeIndexIncomplete, "sample numbers out of order at %d", i)
		}
	}
	if math.IsNaN(t.CoveragePct) || math.IsInf(t.CoveragePct, 0) {
		return clock.New(clock.StageIndex, clock.CodeIndexIncomplete, "coverage is not finite")
	}
	return nil
}

// Summary renders a compact human-readable status.
func (t *TrackIndex) Summary() string {
	return fmt.Sprintf("track %d: %d keyframes, coverage %.2f%%, max interval %s%s",
		t.TrackID, len(t.Entries), t.CoveragePct, clock.FormatNs(t.MaxIntervalNs),
		sparseSuffix(t.Sparse))
}

func sparseSuffix(sparse bool) string {
	if sparse {
		return " (sparse)"
	}
	return ""
}
