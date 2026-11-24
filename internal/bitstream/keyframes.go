// Package bitstream analyzes per-track frame cadence and keyframe spacing
// on top of the explicit timeline. It distinguishes decodable sync
// boundaries (backed by stss/decoded tables) from inferred boundaries
// (truncated or missing indexes), so keyframe-gap statistics never present
// a guess as a measured value.
package bitstream

import (
	"sort"

	"mediacheck/internal/media"
)

// GapStats summarizes keyframe spacing for one track, in milliseconds.
type GapStats struct {
	TrackID       uint32 `json:"track_id"`
	KeyframeCount int    `json:"keyframe_count"`
	// GapsMs are the successive inter-keyframe intervals.
	GapsMs []int64 `json:"gaps_ms"`
	MinMs  int64   `json:"min_ms"`
	MaxMs  int64   `json:"max_ms"`
	MeanMs float64 `json:"mean_ms"`
	// P95Ms is the 95th percentile gap (nearest-rank method).
	P95Ms int64 `json:"p95_ms"`
	// Deterministic is false when any gap touches an inferred boundary.
	Deterministic bool `json:"deterministic"`
}

// Anomaly is one abnormal keyframe interval on the timeline.
type Anomaly struct {
	TrackID       uint32 `json:"track_id"`
	SampleIndex   int    `json:"sample_index"`
	StartNanos    int64  `json:"start_nanos"`
	GapMs         int64  `json:"gap_ms"`
	Deterministic bool   `json:"deterministic"`
	Reason        string `json:"reason"`
}

// Boundary classifies a sync point's trust level.
type Boundary struct {
	TrackID     uint32 `json:"track_id"`
	SampleIndex int    `json:"sample_index"`
	StartNanos  int64  `json:"start_nanos"`
	Decodable   bool   `json:"decodable"`
}

// Report is the full keyframe analysis for one track.
type Report struct {
	Stats      GapStats   `json:"stats"`
	Anomalies  []Anomaly  `json:"anomalies"`
	Boundaries []Boundary `json:"boundaries"`
}

// Analyze computes keyframe spacing from a track timeline. Cadence breaks
// are detected robustly with median/MAD (see findAnomalies); inferred
// boundaries are listed separately and never counted as decodable.
func Analyze(line *media.TrackLine) Report {
	rep := Report{Stats: GapStats{TrackID: line.TrackID, Deterministic: true}}
	syncIdx := make([]int, 0)
	for i, p := range line.Points {
		if !p.IsSync {
			continue
		}
		syncIdx = append(syncIdx, i)
		rep.Boundaries = append(rep.Boundaries, Boundary{
			TrackID:     line.TrackID,
			SampleIndex: i,
			StartNanos:  p.StartNanos,
			Decodable:   p.Source == media.SourceDecoded,
		})
		if p.Source != media.SourceDecoded {
			rep.Stats.Deterministic = false
		}
	}
	rep.Stats.KeyframeCount = len(syncIdx)
	if len(syncIdx) < 2 {
		return rep
	}
	gaps := make([]int64, 0, len(syncIdx)-1)
	det := make([]int64, 0, len(syncIdx)-1)
	for k := 1; k < len(syncIdx); k++ {
		a, b := line.Points[syncIdx[k-1]], line.Points[syncIdx[k]]
		gapMs := (b.StartNanos - a.StartNanos) / 1_000_000
		gaps = append(gaps, gapMs)
		if a.Source == media.SourceDecoded && b.Source == media.SourceDecoded {
			det = append(det, gapMs)
		} else {
			rep.Stats.Deterministic = false
		}
	}
	rep.Stats.GapsMs = gaps
	rep.Stats.MinMs, rep.Stats.MaxMs, rep.Stats.MeanMs, rep.Stats.P95Ms = summarize(gaps)
	rep.Anomalies = findAnomalies(line, syncIdx, det)
	return rep
}

// summarize returns min, max, mean and nearest-rank P95 in milliseconds.
func summarize(gaps []int64) (min, max int64, mean float64, p95 int64) {
	sorted := append([]int64(nil), gaps...)
	sortInt64(sorted)
	min, max = sorted[0], sorted[len(sorted)-1]
	var sum int64
	for _, g := range gaps {
		sum += g
	}
	mean = float64(sum) / float64(len(gaps))
	rank := (95*len(gaps) + 99) / 100 // ceil(0.95*n), 1-based
	if rank < 1 {
		rank = 1
	}
	p95 = sorted[rank-1]
	return min, max, mean, p95
}

// findAnomalies flags non-positive gaps and deterministic gaps that deviate
// from normal cadence. Normal cadence is estimated robustly with the median
// and median absolute deviation (MAD): a gap beyond median + 4*MAD is a
// cadence break. With zero MAD (near-constant cadence) it falls back to a
// 2x-median rule, which keeps small-sample fixtures meaningful.
func findAnomalies(line *media.TrackLine, syncIdx []int, detGaps []int64) []Anomaly {
	median := medianOf(detGaps)
	mad := madOf(detGaps, median)
	var out []Anomaly
	for k := 1; k < len(syncIdx); k++ {
		a, b := line.Points[syncIdx[k-1]], line.Points[syncIdx[k]]
		gapMs := (b.StartNanos - a.StartNanos) / 1_000_000
		deterministic := a.Source == media.SourceDecoded && b.Source == media.SourceDecoded
		reason := ""
		switch {
		case gapMs <= 0:
			reason = "non-positive keyframe gap"
		case deterministic && isCadenceBreak(gapMs, median, mad):
			reason = "keyframe gap exceeds robust cadence threshold"
		}
		if reason != "" {
			out = append(out, Anomaly{
				TrackID:       line.TrackID,
				SampleIndex:   syncIdx[k],
				StartNanos:    b.StartNanos,
				GapMs:         gapMs,
				Deterministic: deterministic,
				Reason:        reason,
			})
		}
	}
	return out
}

func isCadenceBreak(gap, median, mad int64) bool {
	if mad > 0 {
		return gap > median+4*mad
	}
	return median > 0 && gap > 2*median
}

func madOf(v []int64, median int64) int64 {
	if len(v) == 0 {
		return 0
	}
	dev := make([]int64, len(v))
	for i, x := range v {
		d := x - median
		if d < 0 {
			d = -d
		}
		dev[i] = d
	}
	return medianOf(dev)
}

func medianOf(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sortInt64(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

func sortInt64(v []int64) {
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
}
