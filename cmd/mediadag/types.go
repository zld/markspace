package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"mediadag/internal/ingest"
)

const nodeVersion = "v1.0.0"

// pipelineOptions toggles strictness for failure-injection scenarios.
type pipelineOptions struct {
	// StrictNAL makes the inspect node fail with input-corrupt when a NAL
	// fragment is truncated or missing, instead of only localizing it.
	StrictNAL bool
}

// IngestResult is the output of the ingest node (RTP media only).
type IngestResult struct {
	Events []ingest.Event
	Stats  ingest.Stats
}

// Summary is the final report-node result; its digest is result_sha256.
type Summary struct {
	InputDigest      string  `json:"input_digest"`
	Params           string  `json:"params"`
	Version          string  `json:"version"`
	TargetKbps       float64 `json:"target_kbps"`
	ActualKbps       float64 `json:"actual_kbps"`
	SentKbps         float64 `json:"sent_kbps"`
	LossRatio        float64 `json:"loss_ratio"`
	DeviationCause   string  `json:"deviation_cause"`
	PacketsReceived  int     `json:"packets_received"`
	LossDetected     int     `json:"loss_detected"`
	Duplicates       int     `json:"duplicates"`
	Late             int     `json:"late"`
	IDRCount         int     `json:"idr_count"`
	SPSCount         int     `json:"sps_count"`
	PPSCount         int     `json:"pps_count"`
	KeyframeIntAvgMs float64 `json:"keyframe_interval_avg_ms"`
	CorruptRanges    int     `json:"corrupt_ranges"`
	ManifestAlignMs  float64 `json:"manifest_alignment_ms"`
	ManifestStartMs  float64 `json:"manifest_start_ms"`
	ManifestEndMs    float64 `json:"manifest_end_ms"`
	Segments         int     `json:"segments"`
	BitrateDevPct    float64 `json:"bitrate_deviation_pct"`
	QualityScore     float64 `json:"quality_score"`
}

// decode unmarshals a persisted node result into *T.
func decode[T any](raw json.RawMessage) (any, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// ntpToTime converts a 64-bit NTP timestamp to wall-clock time.
func ntpToTime(ntp uint64) time.Time {
	sec := int64(ntp >> 32)
	frac := int64(ntp & 0xFFFFFFFF)
	return time.Unix(sec, frac*int64(time.Second)>>32).UTC()
}

// writeError reports a demo failure with a consistent prefix.
func writeError(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "mediadag demo: "+format+"\n", args...)
}
