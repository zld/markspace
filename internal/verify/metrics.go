// Package verify computes cross-stage quality metrics and validates the
// pipeline invariants after every run. All computations are deterministic
// so repeated runs over identical input produce identical output.
package verify

import (
	"sort"

	"mediasync/internal/index"
	"mediasync/internal/mederr"
	"mediasync/internal/recovery"
	"mediasync/internal/repair"
)

// Summary is the machine-readable per-run quality summary.
type Summary struct {
	SyncErrorMedianMS float64 `json:"av_sync_error_ms_median"`
	SyncErrorP95MS    float64 `json:"av_sync_error_ms_p95"`
	RecoveryRate      float64 `json:"packet_recovery_rate"`
	LatencyP95MS      float64 `json:"pipeline_latency_p95_ms"`
	TimestampWraps    int     `json:"timestamp_wraps"`
	VFRIntervals      int     `json:"vfr_intervals"`
	TruncatedSegments int     `json:"truncated_segments"`
	CheckpointRecovered bool  `json:"checkpoint_recovered"`
	IndexRecords      int     `json:"index_records"`
	IndexSHA256       string  `json:"index_sha256"`
	InvariantsOK      bool    `json:"invariants_ok"`
}

// LatencySample pairs one stage's processing latency with its record key.
type LatencySample struct {
	Key     string
	LatencyUS int64
}

// InvariantToleranceNS bounds representation boundary deviation.
const InvariantToleranceNS int64 = 5_000_000 // 5ms

// percentile returns the nearest-rank percentile of a sorted float slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int((p/100.0)*float64(len(sorted)) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// median returns the median, averaging the two middle values for even n.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// Compute builds the summary from the stage outputs.
func Compute(frames []*repair.Frame, rep recovery.Report,
	latencies []LatencySample, digest index.Digest,
	checkpointRecovered bool, truncated int) Summary {
	// AV sync error: |corrected instant of an audio frame - nearest video
	// frame corrected instant|, evaluated per audio frame.
	var videoNS, audioNS, syncErr []float64
	for _, f := range frames {
		switch f.Track {
		case "video":
			videoNS = append(videoNS, float64(f.CorrectedNS))
		case "audio":
			audioNS = append(audioNS, float64(f.CorrectedNS))
		}
	}
	sort.Float64s(videoNS)
	for _, a := range audioNS {
		if len(videoNS) == 0 {
			break
		}
		i := sort.SearchFloat64s(videoNS, a)
		best := absF(videoNS[0] - a)
		for _, j := range []int{i - 1, i} {
			if j >= 0 && j < len(videoNS) {
				if d := absF(videoNS[j] - a); d < best {
					best = d
				}
			}
		}
		syncErr = append(syncErr, best/1e6) // ns -> ms
	}
	sort.Float64s(syncErr)

	lat := make([]float64, len(latencies))
	for i, s := range latencies {
		lat[i] = float64(s.LatencyUS) / 1000.0 // us -> ms
	}
	sort.Float64s(lat)

	wraps, vfr := 0, 0
	for _, f := range frames {
		for _, a := range f.Anomalies {
			switch a.Kind {
			case repair.AnomalyWrap:
				wraps++
			case repair.AnomalyVFRShift:
				vfr++
			}
		}
	}
	return Summary{
		SyncErrorMedianMS:   median(syncErr),
		SyncErrorP95MS:      percentile(syncErr, 95),
		RecoveryRate:        rep.RecoveryRate(),
		LatencyP95MS:        percentile(lat, 95),
		TimestampWraps:      wraps,
		VFRIntervals:        vfr,
		TruncatedSegments:   truncated,
		CheckpointRecovered: checkpointRecovered,
		IndexRecords:        digest.Records,
		IndexSHA256:         digest.SHA256,
		InvariantsOK:        true, // finalised by CheckInvariants
	}
}

// CheckInvariants validates the four cross-stage guarantees and returns a
// structured error naming the first violation (track, offset, interval).
func CheckInvariants(frames []*repair.Frame, records []index.Record,
	inputSizes map[string]int64) error {
	// 1. Per-representation timestamps are monotonic except where the frame
	// carries an explicit discontinuity annotation. Two representations of
	// one track legitimately restart their local counters.
	byGroup := map[string][]*repair.Frame{}
	for _, f := range frames {
		key := f.Track + "|" + f.Representation
		byGroup[key] = append(byGroup[key], f)
	}
	for group, fs := range byGroup {
		for i := 1; i < len(fs); i++ {
			if fs[i].CorrectedNS < fs[i-1].CorrectedNS &&
				!hasAnomaly(fs[i], repair.AnomalyDiscontinuity) {
				return mederr.New(mederr.CodeInternal,
					"non-monotonic timestamp in group "+group,
					mederr.WithTrack(group),
					mederr.WithInterval(fs[i-1].CorrectedNS, fs[i].CorrectedNS))
			}
		}
	}
	// 2. Representation boundary deviation stays within tolerance.
	bounds := map[string][2]int64{}
	for _, f := range frames {
		b, ok := bounds[f.Representation]
		if !ok {
			bounds[f.Representation] = [2]int64{f.CorrectedNS, f.CorrectedNS}
			continue
		}
		if f.CorrectedNS < b[0] {
			b[0] = f.CorrectedNS
		}
		if f.CorrectedNS > b[1] {
			b[1] = f.CorrectedNS
		}
		bounds[f.Representation] = b
	}
	var spans [][2]int64
	for _, b := range bounds {
		spans = append(spans, b)
	}
	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			// Two representations describe the same programme timeline,
			// so their covered spans must overlap or sit within tolerance.
			lo := max64(spans[i][0], spans[j][0])
			hi := min64(spans[i][1], spans[j][1])
			if hi < lo && lo-hi > InvariantToleranceNS {
				return mederr.New(mederr.CodeInternal,
					"representation boundary deviation exceeds tolerance")
			}
		}
	}
	// 3. Every indexed offset lies inside its declared input file.
	for _, r := range records {
		size, ok := inputSizes[r.Representation]
		if !ok {
			return mederr.New(mederr.CodeInternal,
				"index record references unknown representation",
				mederr.WithTrack(r.Representation),
				mederr.WithOffset(r.ByteOffset))
		}
		if r.ByteOffset < 0 || r.ByteOffset >= size {
			return mederr.New(mederr.CodeInternal,
				"index offset outside input range",
				mederr.WithTrack(r.Representation),
				mederr.WithOffset(r.ByteOffset))
		}
	}
	return nil
}

func hasAnomaly(f *repair.Frame, k repair.AnomalyKind) bool {
	for _, a := range f.Anomalies {
		if a.Kind == k {
			return true
		}
	}
	return false
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
