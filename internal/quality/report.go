package quality

import (
	"math"
	"sort"

	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/stat"
)

// Report is the stable, JSON-serializable quality summary for a full job.
type Report struct {
	MeanPSNRDB        float64   `json:"psnr_mean_db"`
	MinPSNRDB         float64   `json:"psnr_min_db"`
	MaxPSNRDB         float64   `json:"psnr_max_db"`
	SegmentPSNRDB     []float64 `json:"segment_psnr_db"`
	SegmentDeltaDB    []float64 `json:"segment_psnr_delta_db"`
	WorstSegment      int       `json:"worst_segment"`
	WorstSegmentDB    float64   `json:"worst_segment_psnr_db"`
	BlackFrameTotal   int       `json:"black_frames"`
	IdenticalFrameTotal int     `json:"identical_frame_pairs"`
	MeasuredSegments  int       `json:"measured_segments"`
}

// finiteOnly drops NaN/Inf values; indices are returned alongside so callers
// can map back to segment numbers.
func finiteOnly(values []float64) ([]float64, []int) {
	out := make([]float64, 0, len(values))
	idx := make([]int, 0, len(values))
	for i, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out = append(out, v)
			idx = append(idx, i)
		}
	}
	return out, idx
}

// BuildReport combines per-segment summaries. Segment indices are 1-based in
// user-facing fields. Segments without any finite measurement contribute
// NaN placeholders in the ordered arrays but never to numeric aggregates;
// adjacent deltas are computed only between two finite neighbors, keeping
// the delta array length equal to len(segments)-1.
func BuildReport(segments []SegmentSummary) Report {
	r := Report{
		SegmentPSNRDB:  make([]float64, len(segments)),
		SegmentDeltaDB: make([]float64, 0, max0(len(segments)-1)),
		WorstSegment:   -1,
		WorstSegmentDB: math.NaN(),
	}
	for i, s := range segments {
		r.SegmentPSNRDB[i] = s.MeanDB
		r.BlackFrameTotal += s.BlackPairs
		r.IdenticalFrameTotal += s.IdenticalPairs
	}

	finite, finiteIdx := finiteOnly(r.SegmentPSNRDB)
	r.MeasuredSegments = len(finite)
	if len(finite) > 0 {
		r.MeanPSNRDB = stat.Mean(finite, nil)
		r.MinPSNRDB = floats.Min(finite)
		r.MaxPSNRDB = floats.Max(finite)
		// Worst segment: lowest finite mean; ties resolve to lower index.
		worstVal := math.Inf(1)
		for k, v := range finite {
			if v < worstVal {
				worstVal = v
				r.WorstSegment = finiteIdx[k] + 1
				r.WorstSegmentDB = v
			}
		}
	} else {
		r.MeanPSNRDB = math.NaN()
		r.MinPSNRDB = math.NaN()
		r.MaxPSNRDB = math.NaN()
	}

	for i := 0; i+1 < len(r.SegmentPSNRDB); i++ {
		a, b := r.SegmentPSNRDB[i], r.SegmentPSNRDB[i+1]
		if math.IsNaN(a) || math.IsInf(a, 0) || math.IsNaN(b) || math.IsInf(b, 0) {
			r.SegmentDeltaDB = append(r.SegmentDeltaDB, math.NaN())
			continue
		}
		r.SegmentDeltaDB = append(r.SegmentDeltaDB, math.Abs(a-b))
	}
	return r
}

// RankedWorst returns segment indices (1-based) ordered by ascending PSNR,
// giving callers a stable ranking for text reports.
func (r Report) RankedWorst() []int {
	type pair struct {
		idx int
		val float64
	}
	pairs := make([]pair, 0, len(r.SegmentPSNRDB))
	for i, v := range r.SegmentPSNRDB {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			pairs = append(pairs, pair{i + 1, v})
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool {
		if pairs[a].val != pairs[b].val {
			return pairs[a].val < pairs[b].val
		}
		return pairs[a].idx < pairs[b].idx
	})
	out := make([]int, len(pairs))
	for i, p := range pairs {
		out[i] = p.idx
	}
	return out
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
