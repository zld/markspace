package main

import (
	"fmt"

	"mediaroute/internal/export"
	"mediaroute/internal/pipeline"
)

func printMetrics(m *pipeline.Metrics, manifest *export.Manifest) {
	fmt.Printf("roundtrip_rate=%.2f\n", m.RoundtripRate)
	fmt.Printf("keyframe_hit_rate=%.2f\n", m.KeyframeHitRate)
	fmt.Printf("segments=%d\n", m.Segments)
	fmt.Printf("segment_sha256_match=%d/%d\n", m.SegmentMatches, m.Segments)
	fmt.Printf("duplicates=%d (duplicates>=1: %t)\n", m.Duplicates, m.Duplicates >= 1)
	fmt.Printf("reordered=%d (reordered>=1: %t)\n", m.Reordered, m.Reordered >= 1)
	fmt.Printf("lost_burst=%d\n", m.Lost)
	fmt.Printf("congestion_peak=%d\n", m.CongestionPeak)
	fmt.Printf("gaps=%d truncated_frames=%d\n", m.Gaps, m.TruncatedFrames)
	fmt.Printf("recovered=%t\n", m.Recovered)
	fmt.Printf("repeat_hash_match=%t\n", m.RepeatHashMatch)
	if manifest != nil {
		for _, d := range manifest.Segments {
			fmt.Printf("segment %s frames=%d sha256=%s\n", d.SegmentID, d.FrameCount, d.SHA256)
		}
	}
}
