package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// GOPBucket is one histogram entry of keyframe intervals.
type GOPBucket struct {
	IntervalTicks int64 `json:"interval_ticks"`
	Count         int   `json:"count"`
}

// SegmentReport is the deterministic, checkpointable result for one
// manifest-referenced media segment.
type SegmentReport struct {
	URI              string  `json:"uri"`
	Kind             string  `json:"kind"` // progressive | fragmented | initialization
	Period           int     `json:"period"`
	Timescale        uint32  `json:"timescale"`
	Bytes            int64   `json:"bytes"`
	SampleCount      int64   `json:"sample_count"`
	SyncSamples      []int64 `json:"sync_samples"`
	GOPIntervals     []int64 `json:"gop_intervals_ticks"`
	GOPMinTicks      int64   `json:"gop_min_ticks"`
	GOPMaxTicks      int64   `json:"gop_max_ticks"`
	GlobalStartTicks int64   `json:"global_start_ticks"`
	GlobalEndTicks   int64   `json:"global_end_ticks"`
	DamageStartTicks int64   `json:"damage_start_ticks"`
	DamageEndTicks   int64   `json:"damage_end_ticks"`
	WrapEvents       int     `json:"wrap_events"`
	DriftPPM         int64   `json:"drift_ppm"`
	ContentSHA256    string  `json:"content_sha256"`
	Faults           []Fault `json:"faults"`
	Complete         bool    `json:"complete"`
}

// Report is the full deterministic analysis result. Runtime metrics
// (elapsed time, memory, resume position) are deliberately NOT part of the
// canonical hashed document.
type Report struct {
	Input             string          `json:"input"`
	InputKind         string          `json:"input_kind"` // hls | dash
	UnifiedTimescale  uint32          `json:"unified_timescale"`
	PeriodCount       int             `json:"period_count"`
	SegmentCount      int             `json:"segment_count"`
	TotalSamples      int64           `json:"total_samples"`
	GOPMinTicks       int64           `json:"gop_min_ticks"`
	GOPMaxTicks       int64           `json:"gop_max_ticks"`
	GOPHistogram      []GOPBucket     `json:"gop_histogram"`
	RepresentationIDs []string        `json:"representation_ids,omitempty"`
	Segments          []SegmentReport `json:"segments"`
	Faults            []Fault         `json:"faults"`
	WarningCount      int             `json:"warning_count"`
	ErrorCount        int             `json:"error_count"`
	SHA256            string          `json:"sha256"`
}

// CanonicalBytes returns the stable JSON encoding used for checksums. Faults
// and histogram buckets are sorted so map/encounter order can never affect
// the digest.
func (r *Report) CanonicalBytes() ([]byte, error) {
	cp := *r
	cp.SHA256 = ""
	cp.Faults = SortedFaults(append([]Fault(nil), r.Faults...))
	for i := range cp.Segments {
		cp.Segments[i].Faults = SortedFaults(append([]Fault(nil), cp.Segments[i].Faults...))
	}
	sort.SliceStable(cp.GOPHistogram, func(i, j int) bool {
		return cp.GOPHistogram[i].IntervalTicks < cp.GOPHistogram[j].IntervalTicks
	})
	return json.Marshal(cp)
}

// ComputeSHA256 fills and returns the canonical result checksum.
func (r *Report) ComputeSHA256() (string, error) {
	b, err := r.CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	r.SHA256 = hex.EncodeToString(sum[:])
	return r.SHA256, nil
}

// SortedFaults gives a deterministic fault ordering: segment, offset, code.
func SortedFaults(fs []Fault) []Fault {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Segment != fs[j].Segment {
			return fs[i].Segment < fs[j].Segment
		}
		if fs[i].OffsetBytes != fs[j].OffsetBytes {
			return fs[i].OffsetBytes < fs[j].OffsetBytes
		}
		if fs[i].TimeStart != fs[j].TimeStart {
			return fs[i].TimeStart < fs[j].TimeStart
		}
		return fs[i].Code < fs[j].Code
	})
	return fs
}

// AggregateGOP folds per-segment intervals into global min/max/histogram.
// Intervals must already exclude discontinuity boundaries.
func AggregateGOP(segments []SegmentReport) (minV, maxV int64, histogram []GOPBucket) {
	minV, maxV = Unknown, Unknown
	counts := map[int64]int{}
	for _, s := range segments {
		for _, v := range s.GOPIntervals {
			if v <= 0 {
				continue
			}
			if minV == Unknown || v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
			counts[v]++
		}
	}
	for v, c := range counts {
		histogram = append(histogram, GOPBucket{IntervalTicks: v, Count: c})
	}
	sort.Slice(histogram, func(i, j int) bool { return histogram[i].IntervalTicks < histogram[j].IntervalTicks })
	return minV, maxV, histogram
}
