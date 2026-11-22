package main

import (
	"fmt"

	"github.com/pairwise/mediad/internal/verify"
)

// printSummary prints the acceptance fields. Every field required by the
// hand-off contract is on its own line so operators can diff runs.
func printSummary(s verify.Summary) {
	fmt.Printf("roundtrip_rate:      %.4f\n", s.RoundtripRate)
	fmt.Printf("keyframe_hit_rate:   %.4f\n", s.KeyframeHitRate)
	fmt.Printf("segments:            %d\n", s.Segments)
	fmt.Printf("recovered_segments:  %d\n", s.RecoveredSegments)
	fmt.Printf("duplicate_segments:  %d\n", s.DuplicateSegments)
	fmt.Printf("segment_sha256:      %s\n", s.SegmentSHA256)
	if s.ContinuousHash != "" || s.ResumedHash != "" {
		fmt.Printf("continuous_manifest_sha256: %s\n", s.ContinuousHash)
		fmt.Printf("resumed_manifest_sha256:    %s\n", s.ResumedHash)
		fmt.Printf("resume_identical:           %v\n", s.ResumeIdentical)
	}
}
