package timeline

import (
	"sort"

	"mediacore/internal/fault"
)

// GapPolicy controls what happens when the merged timeline has holes.
type GapPolicy string

const (
	// GapRecord leaves the hole in place and records it. No synthetic samples
	// are invented, so provenance remains exact. This is the only supported
	// policy: a track is marked unmergeable by the repair stage when ordering
	// cannot be inferred, not because time is missing.
	GapRecord GapPolicy = "record"
)

// Gap is a hole in the synthesized timeline.
type Gap struct {
	Start         int64  `json:"start"`
	End           int64  `json:"end"`
	Duration      int64  `json:"duration"`
	TrackID       uint32 `json:"track_id,omitempty"`
	Kind          string `json:"kind"` // "internal" or "global"
	AfterFragment int    `json:"after_fragment"`
}

// MergeResult is the ordered multi-track timeline.
type MergeResult struct {
	Timescale   uint32    `json:"timescale"`
	Samples     []Sample  `json:"samples"`
	Tracks      []*Track  `json:"tracks"`
	Gaps        []Gap     `json:"gaps"`
	Duration    int64     `json:"duration"`
	TrackOrder  []uint32  `json:"track_order"`
	Unmergeable []uint32  `json:"unmergeable"`
	Policy      GapPolicy `json:"policy"`
}

// Merge sorts track samples onto one sequence and records gaps. Tracks marked
// unmergeable are retained with their errors but excluded from the ordered
// sample sequence.
func Merge(tracks []*Track, policy GapPolicy) (*MergeResult, []*fault.Error) {
	if policy == "" {
		policy = GapRecord
	}
	res := &MergeResult{Timescale: tracksTimescale(tracks), Policy: policy}
	var errs []*fault.Error
	for _, tr := range tracks {
		if !tr.Mergeable {
			res.Unmergeable = append(res.Unmergeable, tr.ID)
			errs = append(errs, tr.Errors...)
			continue
		}
		res.TrackOrder = append(res.TrackOrder, tr.ID)
		res.Samples = append(res.Samples, tr.Samples...)
	}
	sort.SliceStable(res.Samples, func(i, j int) bool {
		a, b := res.Samples[i], res.Samples[j]
		if a.TimelineTime != b.TimelineTime {
			return a.TimelineTime < b.TimelineTime
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		if a.FragmentIndex != b.FragmentIndex {
			return a.FragmentIndex < b.FragmentIndex
		}
		return a.SampleIndex < b.SampleIndex
	})
	for _, tr := range tracks {
		if !tr.Mergeable {
			continue
		}
		res.Gaps = append(res.Gaps, trackGaps(tr)...)
	}
	sort.SliceStable(res.Gaps, func(i, j int) bool {
		if res.Gaps[i].Start != res.Gaps[j].Start {
			return res.Gaps[i].Start < res.Gaps[j].Start
		}
		return res.Gaps[i].TrackID < res.Gaps[j].TrackID
	})
	res.Duration = coverageEnd(res.Samples)
	res.Tracks = tracks
	return res, errs
}

func tracksTimescale(tracks []*Track) uint32 {
	for _, tr := range tracks {
		if tr.Mapping.TimelineTimescale != 0 {
			return tr.Mapping.TimelineTimescale
		}
	}
	return 0
}

func trackGaps(tr *Track) []Gap {
	var gaps []Gap
	for i := 1; i < len(tr.Samples); i++ {
		prev, next := tr.Samples[i-1], tr.Samples[i]
		if next.TimelineTime <= prev.End() {
			continue
		}
		// Overlap or exact continuity is not a gap; only uncovered time is.
		if next.TimelineTime > prev.End() {
			gaps = append(gaps, Gap{
				Start:         prev.End(),
				End:           next.TimelineTime,
				Duration:      next.TimelineTime - prev.End(),
				TrackID:       tr.ID,
				Kind:          "internal",
				AfterFragment: prev.FragmentIndex,
			})
		}
	}
	return gaps
}

func coverageEnd(samples []Sample) int64 {
	var end int64
	for _, s := range samples {
		if e := s.End(); e > end {
			end = e
		}
	}
	return end
}

// GlobalGaps returns holes not covered by any merged sample. Tracks may
// legitimately interleave, so a hole only exists where no sample is active.
func GlobalGaps(samples []Sample) []Gap {
	if len(samples) == 0 {
		return nil
	}
	sorted := append([]Sample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TimelineTime < sorted[j].TimelineTime })
	var gaps []Gap
	covered := sorted[0].End()
	for _, s := range sorted[1:] {
		if s.TimelineTime > covered {
			gaps = append(gaps, Gap{Start: covered, End: s.TimelineTime, Duration: s.TimelineTime - covered, Kind: "global"})
		}
		if s.End() > covered {
			covered = s.End()
		}
	}
	return gaps
}
