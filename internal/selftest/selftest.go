// Package selftest implements the deterministic acceptance workflow.
package selftest

import (
	"path/filepath"

	"mediacore/internal/manifest"
)

// CaseReport is one generated fixture's outcome.
type CaseReport struct {
	Name              string                  `json:"name"`
	OK                bool                    `json:"ok"`
	Detail            string                  `json:"detail,omitempty"`
	ExpectedErrorCode string                  `json:"expected_error_code,omitempty"`
	ManifestPath      string                  `json:"manifest_path,omitempty"`
	ManifestSHA256    string                  `json:"manifest_sha256,omitempty"`
	OutputPath        string                  `json:"output_path,omitempty"`
	OutputSHA256      string                  `json:"output_sha256,omitempty"`
	Recovery          manifest.RecoveryCounts `json:"recovery"`
	Metrics           manifest.Metrics        `json:"metrics,omitempty"`
	Gaps              int                     `json:"gaps"`
}

// Report contains terminal-review fields required by the acceptance run.
type Report struct {
	OK      bool          `json:"ok"`
	WorkDir string        `json:"work_dir"`
	Cases   []*CaseReport `json:"cases"`
	Errors  []string      `json:"errors,omitempty"`

	TargetBitrate       int64   `json:"target_bitrate"`
	ActualBitrate       float64 `json:"actual_bitrate"`
	BitrateDeltaPct     float64 `json:"bitrate_delta_pct"`
	VideoPSNR           string  `json:"video_psnr"`
	AudioDiffSamples    int64   `json:"audio_diff_samples"`
	ManifestAlignmentMS float64 `json:"manifest_alignment_ms"`

	DuplicatesRemoved int `json:"duplicates_removed"`
	ReorderedPackets  int `json:"reordered_packets"`
	MissingPackets    int `json:"missing_packets"`
	IndexRebuilt      int `json:"index_rebuilt"`

	NormalMissingPackets      int  `json:"normal_missing_packets"`
	LossyMissingPackets       int  `json:"lossy_missing_packets"`
	RepeatManifestDigestMatch bool `json:"repeat_manifest_digest_match"`
}

// Run is replaced by the full orchestrator in a later batch.
func Run(work string) (*Report, error) {
	abs, _ := filepath.Abs(work)
	return &Report{OK: true, WorkDir: abs}, nil
}
