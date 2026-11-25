package demo

import (
	"encoding/json"
	"fmt"
	"math"

	"mediacore/internal/pipeline"
)

const defaultRangeText = "00:10:00,00:10:12"

func assertGenerated(rep *pipeline.Report, rangeText string, recovery bool) (bool, string) {
	if rep == nil {
		return false, "nil-report"
	}
	if math.Abs(rep.ParseRoundtripRate-1) > 1e-12 {
		return false, "parse-roundtrip-rate-not-100"
	}
	if math.Abs(rep.KeyframeHitRate-1) > 1e-12 {
		return false, "keyframe-hit-rate-not-100"
	}
	if len(rep.SegmentHashes) != 3 {
		return false, "expected-three-segment-hashes"
	}
	if rep.KeyframeNumber == 0 || rep.QuerySampleCount == 0 {
		return false, "missing-query-result"
	}
	if rangeText == defaultRangeText && rep.QuerySampleCount != 12 {
		return false, fmt.Sprintf("expected-12-samples-got-%d", rep.QuerySampleCount)
	}
	if recovery {
		if rep.RecoveredArtifacts != 1 {
			return false, fmt.Sprintf("expected-recovered_artifacts=1-got-%d", rep.RecoveredArtifacts)
		}
		if rep.DuplicatePublications != 0 {
			return false, fmt.Sprintf("expected-duplicate_publications=0-got-%d", rep.DuplicatePublications)
		}
	}
	return true, ""
}

func printReport(rep *pipeline.Report) {
	if rep == nil {
		fmt.Println("report=null")
		return
	}
	fmt.Printf("parse_roundtrip_rate=%.4f\n", rep.ParseRoundtripRate)
	fmt.Printf("keyframe_hit_rate=%.4f\n", rep.KeyframeHitRate)
	hashes, _ := json.Marshal(rep.SegmentHashes)
	fmt.Printf("segment_hashes=%s\n", hashes)
	fmt.Printf("recovered_artifacts=%d\n", rep.RecoveredArtifacts)
	fmt.Printf("duplicate_publications=%d\n", rep.DuplicatePublications)
	offsets := rep.ErrorOffsets
	if offsets == nil {
		offsets = []pipeline.ErrorOffset{}
	}
	offsetJSON, _ := json.Marshal(offsets)
	fmt.Printf("error_offsets=%s\n", offsetJSON)
	fmt.Printf("range_samples=%d range_keyframe=%d publish_writes=%d regenerated_artifacts=%d\n",
		rep.QuerySampleCount, rep.KeyframeNumber, rep.PublishWrites, rep.RegeneratedArtifacts)
}

func printCorruptReport(rep *CorruptReport) {
	if rep == nil {
		fmt.Println("corrupt_report=null")
		fmt.Println("verified_marker=false")
		return
	}
	offsets := make([]ErrorOffsetView, 0, len(rep.Results))
	for _, r := range rep.Results {
		status := "ok"
		if !r.OK {
			status = "mismatch"
		}
		fmt.Printf("corrupt_case=%q category=%s expected=%s raw_offset=%d track=%d status=%s\n",
			r.Name, r.Category, r.Expected, r.Offset, r.Track, status)
		offsets = append(offsets, ErrorOffsetView{
			Category: r.Category, Path: r.Path, Track: r.Track, Offset: r.Offset,
		})
	}
	data, _ := json.MarshalIndent(offsets, "", "  ")
	fmt.Println("error_offsets=" + string(data))
	fmt.Printf("verified_marker=%t\n", rep.VerifiedMarker)
}

// ErrorOffsetView is the JSON row printed by corrupt mode.
type ErrorOffsetView struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Track    int    `json:"track_number"`
	Offset   int64  `json:"raw_offset"`
}
