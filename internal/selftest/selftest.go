// Package selftest implements the deterministic acceptance workflow.
package selftest

import (
	"fmt"
	"os"
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

// Report contains all terminal-review fields required by the acceptance run.
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

// Run generates fixtures, executes recovery/remux/verification, and aggregates.
func Run(work string) (*Report, error) {
	if work == "" {
		work = "./tmp"
	}
	abs, err := filepath.Abs(work)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"inputs", "outputs", "manifests", "cache"} {
		if err := os.MkdirAll(filepath.Join(abs, d), 0o755); err != nil {
			return nil, err
		}
	}
	r := &Report{OK: true, WorkDir: abs, VideoPSNR: "n/a"}

	normal, err := runNormalCase(abs)
	r.add(normal, err)

	truncated, err := runTruncatedCase(abs)
	r.add(truncated, err)

	lossy, err := runRTPCase(abs)
	r.add(lossy, err)

	changed, err := runChangedCase(abs)
	r.add(changed, err)

	r.aggregate(normal, lossy, changed)
	if !r.OK {
		return r, fmt.Errorf("mediacore selftest failed: %d assertion/process error(s)", len(r.Errors))
	}
	return r, nil
}

func (r *Report) add(c *CaseReport, err error) {
	if c == nil {
		c = &CaseReport{Name: "unknown", OK: false}
	}
	if err != nil {
		c.OK = false
		r.OK = false
		r.Errors = append(r.Errors, err.Error())
	}
	r.Cases = append(r.Cases, c)
}

func (r *Report) aggregate(normal, lossy, changed *CaseReport) {
	if normal != nil {
		r.TargetBitrate = normal.Metrics.TargetBitrate
		r.ActualBitrate = normal.Metrics.ActualBitrate
		r.BitrateDeltaPct = normal.Metrics.BitrateDeltaPct
		r.VideoPSNR = normal.Metrics.VideoPSNR
		r.AudioDiffSamples = normal.Metrics.AudioDiffSamples
		r.ManifestAlignmentMS = normal.Metrics.ManifestAlignmentMS
		r.NormalMissingPackets = normal.Recovery.MissingPackets
		r.RepeatManifestDigestMatch = normal.Detail == "repeat-manifest-digest-match"
	}
	if lossy != nil {
		r.DuplicatesRemoved = lossy.Recovery.DuplicatesRemoved
		r.ReorderedPackets = lossy.Recovery.ReorderedPackets
		r.MissingPackets = lossy.Recovery.MissingPackets
		r.LossyMissingPackets = lossy.Recovery.MissingPackets
	}
	if changed != nil {
		r.IndexRebuilt = changed.Recovery.IndexRebuilt
	}
}
