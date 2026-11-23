package app

import (
	"fmt"

	"github.com/zld/media-check/transport"
)

// demoReport is the acceptance summary printed by the demo command.
type demoReport struct {
	RoundtripParse          float64
	KeyframeHit             float64
	SegmentMatch            float64
	Tracks                  int
	Segments                int
	Duplicates              int
	Reordered               int
	Lost                    int
	CancelledTasks          int
	RepeatManifestMatch     bool
	SourcePath              string
	OutputPath              string
	ManifestPath            string
	ManifestSHA             string
	SourceSHA               string
	OutputSHA               string
	GapRecords              []transport.Gap
	MinBps, MeanBps, MaxBps float64
	FinalParallel           int
	FinalQueue              int
	PeakQueue               int
}

func printDemoReport(r *demoReport, verbose bool) {
	fmt.Printf("roundtrip_parse=%.2f%%\n", r.RoundtripParse*100)
	fmt.Printf("keyframe_hit=%.2f%%\n", r.KeyframeHit*100)
	fmt.Printf("segment_sha256_match=%.2f%%\n", r.SegmentMatch*100)
	fmt.Printf("tracks=%d\n", r.Tracks)
	fmt.Printf("segments=%d\n", r.Segments)
	fmt.Printf("duplicates=%d\n", r.Duplicates)
	fmt.Printf("reordered=%d\n", r.Reordered)
	fmt.Printf("lost=%d\n", r.Lost)
	fmt.Printf("cancelled_tasks=%d\n", r.CancelledTasks)
	fmt.Printf("repeat_manifest_match=%t\n", r.RepeatManifestMatch)
	if verbose {
		fmt.Printf("source=%s\noutput=%s\nmanifest=%s\nmanifest_sha256=%s\nsource_sha256=%s\noutput_sha256=%s\n",
			r.SourcePath, r.OutputPath, r.ManifestPath, r.ManifestSHA, r.SourceSHA, r.OutputSHA)
		fmt.Printf("rtp_gaps=%v\n", r.GapRecords)
		fmt.Printf("bandwidth_bps_min=%.0f bandwidth_bps_mean=%.0f bandwidth_bps_max=%.0f\n", r.MinBps, r.MeanBps, r.MaxBps)
		fmt.Printf("final_parallel=%d final_queue=%d pool_peak_queue=%d\n", r.FinalParallel, r.FinalQueue, r.PeakQueue)
	}
}
