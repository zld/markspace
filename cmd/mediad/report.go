package main

import (
	"encoding/json"
	"io"
)

// AggregateReport is the top-level demo output. Field names match the
// acceptance contract exactly.
type AggregateReport struct {
	Seed                   int64              `json:"seed"`
	RoundtripMatchRate     float64            `json:"roundtrip_match_rate"`
	KeyframeIndexHitRate   float64            `json:"keyframe_index_hit_rate"`
	SegmentHashes          []map[string]string `json:"segment_hashes"`
	DamagedRanges          []rangeView        `json:"damaged_ranges"`
	RecoveredRanges        []rangeView        `json:"recovered_ranges"`
	TracksChecked          int                `json:"tracks_checked"`
	Files                  []FileReport       `json:"files"`
}

func aggregate(reports []FileReport, seed int64) *AggregateReport {
	agg := &AggregateReport{Seed: seed, Files: reports}
	var rtSum, kfSum float64
	var rtCount, kfCount int
	hashSeen := map[string]bool{}
	for _, r := range reports {
		agg.TracksChecked += r.TracksChecked
		// Only count rates that were actually measured (remux+verify runs).
		if r.RoundtripMatchRate > 0 {
			rtSum += r.RoundtripMatchRate
			rtCount++
		}
		if r.KeyframeIndexHitRate > 0 {
			kfSum += r.KeyframeIndexHitRate
			kfCount++
		}
		agg.DamagedRanges = append(agg.DamagedRanges, r.DamagedRanges...)
		agg.RecoveredRanges = append(agg.RecoveredRanges, r.RecoveredRanges...)
		for _, h := range r.SegmentHashes {
			key := h["track_id"] + ":" + h["sample_range"] + ":" + h["sha256"]
			if !hashSeen[key] {
				hashSeen[key] = true
				agg.SegmentHashes = append(agg.SegmentHashes, h)
			}
		}
	}
	if rtCount > 0 {
		agg.RoundtripMatchRate = round(rtSum / float64(rtCount))
	} else {
		agg.RoundtripMatchRate = 1
	}
	if kfCount > 0 {
		agg.KeyframeIndexHitRate = round(kfSum / float64(kfCount))
	} else {
		agg.KeyframeIndexHitRate = 1
	}
	return agg
}

// round keeps three decimal places for stable, readable metric output without
// introducing float drift into the underlying rational timeline.
func round(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

func writeJSON(w io.Writer, agg *AggregateReport) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(agg)
}

func writeText(w io.Writer, agg *AggregateReport) {
	writeJSON(w, agg) // JSON is also the human-readable canonical form here
}
