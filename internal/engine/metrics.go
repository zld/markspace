package engine

import (
	"math"
	"sort"

	"mediarun/internal/pipeline"
)

func digestBytes(b []byte) string { return pipeline.DigestSHA256(b) }

// percentile95 returns the 95th-percentile latency in milliseconds over the
// per-segment processing times. It is zero when nothing was processed.
func percentile95(latencies []int64) int64 {
	if len(latencies) == 0 {
		return 0
	}
	sorted := append([]int64(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// Nearest-rank method, index = ceil(0.95*n) - 1.
	rank := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// maxAVSync measures how far each segment's observed first PCR drifts from its
// ideal timeline slot (segment index * segmentMS). The maximum absolute drift
// in milliseconds is the audio/video synchronization error against the shared
// clock; a clean stream stays well inside the 20 ms acceptance bound.
func maxAVSync(segs []Segment, segmentMS int64) int64 {
	var worst int64
	for _, s := range segs {
		if s.FirstPCRMS < 0 {
			continue
		}
		ideal := int64(s.Index) * segmentMS
		drift := s.FirstPCRMS - ideal
		if drift < 0 {
			drift = -drift
		}
		if drift > worst {
			worst = drift
		}
	}
	return worst
}
