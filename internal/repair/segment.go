// Package repair identifies truncated and corrupted sample boundaries on a
// parsed timeline and segments each track into safe intervals. Data spanning
// a damaged region is never allowed onto the valid timeline.
package repair

import (
	"mediaforensics/internal/model"
)

// RegionKind classifies a detected byte region.
const (
	RegionTruncated = "truncated"
	RegionCorrupt   = "corrupt"
	RegionGap       = "gap"
)

// Region is a contiguous damaged byte range with the samples it affects.
type Region struct {
	Kind         string `json:"kind"`
	TrackID      int    `json:"track_id"`
	StartOffset  int64  `json:"start_offset"`
	EndOffset    int64  `json:"end_offset"`
	FirstSample  int    `json:"first_sample"`
	LastSample   int    `json:"last_sample"` // inclusive
	SourceOffset int64  `json:"source_offset"`
}

// Report is the deterministic output of one repair pass.
type Report struct {
	Regions       []Region               `json:"regions"`
	Excluded      int                    `json:"excluded_samples"`
	Salvaged      int                    `json:"salvaged_samples"`
	TrackCoverage map[int]Coverage       `json:"track_coverage"`
	Errors        []*model.ForensicError `json:"errors"`
}

// Coverage summarizes what fraction of a track survived.
type Coverage struct {
	Total    int `json:"total"`
	Valid    int `json:"valid"`
	Excluded int `json:"excluded"`
}

// Segment walks every track's samples in index order, detecting corrupt
// payloads, missing offsets and boundary overshoots. Adjacent damaged samples
// coalesce into one Region. A one-sample safety margin on each side of a
// corruption is downgraded to low confidence so damaged bytes never feed the
// valid timeline.
func Segment(tl *model.Timeline) *Report {
	rep := &Report{TrackCoverage: map[int]Coverage{}}
	for _, tr := range tl.Tracks {
		cov := Coverage{Total: len(tr.Samples)}
		var damaged []int
		for i, s := range tr.Samples {
			if s.Corrupt || s.Offset < 0 || s.Confidence == model.ConfidenceNone {
				damaged = append(damaged, i)
			}
		}
		rep.addRegions(tr, damaged)
		rep.guardAdjacent(tr, damaged)
		for _, s := range tr.Samples {
			if s.Corrupt || s.Confidence == model.ConfidenceNone || s.Confidence == model.ConfidenceLow {
				cov.Excluded++
			} else {
				cov.Valid++
			}
		}
		rep.Excluded += cov.Excluded
		rep.Salvaged += cov.Valid
		rep.TrackCoverage[tr.ID] = cov
	}
	return rep
}

func (r *Report) addRegions(tr *model.Track, damaged []int) {
	for len(damaged) > 0 {
		first := damaged[0]
		last := first
		k := 1
		for k < len(damaged) && damaged[k] == last+1 {
			last = damaged[k]
			k++
		}
		damaged = damaged[k:]
		region := buildRegion(tr, first, last)
		r.Regions = append(r.Regions, region)
		r.Errors = append(r.Errors, model.NewError(
			model.StageRepair, region.Kind, tr.ID, region.SourceOffset,
			"samples excluded from valid timeline"))
	}
}

func buildRegion(tr *model.Track, first, last int) Region {
	kind := RegionCorrupt
	startS := tr.Samples[first]
	endS := tr.Samples[last]
	if startS.Offset < 0 || endS.Offset < 0 {
		kind = RegionGap
	} else if endS.Offset+endS.Size > trEarliestEnd(tr) {
		kind = RegionTruncated
	}
	return Region{
		Kind:         kind,
		TrackID:      tr.ID,
		FirstSample:  first,
		LastSample:   last,
		StartOffset:  startS.Offset,
		EndOffset:    endS.Offset + endS.Size,
		SourceOffset: startS.Offset,
	}
}

// guardAdjacent downgrades the immediate neighbors of every damaged sample.
// A low-confidence sample is excluded by Track.ValidSamples but retains its
// offset, preserving the audit trail.
func (r *Report) guardAdjacent(tr *model.Track, damaged []int) {
	blocked := map[int]bool{}
	for _, i := range damaged {
		blocked[i] = true
	}
	for i := range tr.Samples {
		if blocked[i] || tr.Samples[i].Corrupt ||
			tr.Samples[i].Confidence == model.ConfidenceNone {
			continue
		}
		if blocked[i-1] || blocked[i+1] {
			tr.Samples[i].Confidence = model.ConfidenceLow
			tr.Samples[i].Basis = "adjacent to damaged region; safety margin"
		}
	}
}

func trEarliestEnd(tr *model.Track) int64 {
	end := int64(0)
	for _, s := range tr.Samples {
		if s.Offset >= 0 && s.Offset+s.Size > end {
			end = s.Offset + s.Size
		}
	}
	return end
}

// SafeIntervals returns the high/medium-confidence sample index ranges that
// the renderer may consume, expressed as half-open [from,to) pairs.
func SafeIntervals(tr *model.Track) [][2]int {
	var out [][2]int
	start := -1
	for i, s := range tr.Samples {
		ok := !s.Corrupt && s.Confidence != model.ConfidenceNone && s.Confidence != model.ConfidenceLow
		if ok && start < 0 {
			start = i
		}
		if (!ok || i == len(tr.Samples)-1) && start >= 0 {
			end := i
			if ok {
				end = i + 1
			}
			if end > start {
				out = append(out, [2]int{start, end})
			}
			start = -1
		}
	}
	return out
}
