package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"avcheck/internal/ingest"
)

// SummaryFile stores the demo result so verify can print the same metrics.
const SummaryFile = "summary.json"

// Summary is the machine-readable acceptance result.
type Summary struct {
	FramesIn             int     `json:"frames_in"`
	Tracks               int     `json:"tracks"`
	DuplicatesDropped    uint64  `json:"duplicates_dropped"`
	ReorderedRecovered   uint64  `json:"reordered_recovered"`
	BurstGaps            uint64  `json:"burst_gaps"`
	CorruptRejected      uint64  `json:"corrupt_rejected"`
	TruncatedRejected    uint64  `json:"truncated_rejected"`
	QueuePeak            int64   `json:"queue_peak"`
	QueueCap             int     `json:"queue_cap"`
	RoundtripConsistency float64 `json:"roundtrip_consistency"`
	KeyframeHitRate      float64 `json:"keyframe_index_hit_rate"`
	Segments             int     `json:"segments"`
	SegmentVerified      int     `json:"segment_sha256_match"`
	RerunManifestMatch   bool    `json:"rerun_manifest_match"`
	ManifestDigest       string  `json:"manifest_digest"`
	ManifestSummary      string  `json:"manifest_summary"`
	Frames               int     `json:"frames_out"`
	ElapsedMS            int64   `json:"elapsed_ms"`
	FrameSHA             string  `json:"frames_sha256"`
	Exit                 int     `json:"exit"`
}

// Summarize reduces a run to the acceptance fields.
func Summarize(r *Result) Summary {
	return Summary{
		FramesIn:             r.Config.Frames * r.Config.Tracks,
		Tracks:               r.Config.Tracks,
		DuplicatesDropped:    uint64(r.Faults.Duplicated),
		ReorderedRecovered:   uint64(r.Faults.Reordered),
		BurstGaps:            uint64(r.Faults.Bursts),
		CorruptRejected:      uint64(r.Faults.Corrupted),
		TruncatedRejected:    uint64(r.Faults.Truncated),
		QueuePeak:            r.QueuePeak,
		QueueCap:             r.QueueCap,
		RoundtripConsistency: r.RoundtripConsistency(),
		KeyframeHitRate:      r.KeyframeHitRate(),
		Segments:             r.Segments,
		Frames:               r.Manifest.Frames,
		ManifestDigest:       r.Manifest.Digest,
		ManifestSummary:      r.Manifest.Summary,
	}
}

// WriteSummary persists summary.json.
func WriteSummary(dir string, s Summary) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SummaryFile), b, 0o644)
}

// LoadSummary reads summary.json.
func LoadSummary(dir string) (Summary, error) {
	var s Summary
	b, err := os.ReadFile(filepath.Join(dir, SummaryFile))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

var _ = fmt.Sprintf

// PolicySkip and PolicyFailTrack are re-exported so the CLI does not need a
// separate ingest import just to name a policy.
const (
	PolicySkip      = ingest.Skip
	PolicyFailTrack = ingest.FailTrack
)
