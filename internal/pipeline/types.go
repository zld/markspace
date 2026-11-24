package pipeline

import (
	"archive/internal/normalize"
	"archive/internal/timeline"
)

// Options controls one demo run.
type Options struct {
	OutDir     string
	Generate   bool
	LossBurst  int
	Bandwidth  []int
	FrameLimit int
}

// DefaultOptions returns the acceptance-command defaults.
func DefaultOptions() Options {
	return Options{
		LossBurst: 3,
		Bandwidth: []int{900, 420, 1100},
	}
}

// Summary is the JSON document printed on stdout.
type Summary struct {
	Command             string         `json:"command"`
	OutputDir           string         `json:"output_dir"`
	NormalSample        string         `json:"normal_sample"`
	Normal              Scenario       `json:"normal"`
	Burst               Scenario       `json:"burst"`
	TargetBitrateKbps   int            `json:"target_bitrate_kbps"`
	ActualBitrateKbps   float64        `json:"actual_bitrate_kbps"`
	BitrateDeviationPct float64        `json:"bitrate_deviation_pct"`
	QualityPSNRDb       float64        `json:"quality_psnr_db"`
	FrameCount          int            `json:"frame_count"`
	FrameSHA256         string         `json:"frame_sha256"`
	ManifestSHA256      string         `json:"manifest_sha256"`
	ManifestAlignmentNS int64          `json:"manifest_alignment_ns"`
	Duplicates          int            `json:"duplicates"`
	Reordered           int            `json:"reordered"`
	Lost                int            `json:"lost"`
	IsolatedLayers      []string       `json:"isolated_layers"`
	RecoveredLayers     []string       `json:"recovered_layers"`
	BandwidthSchedule   []string       `json:"bandwidth_schedule"`
	Boundary            BoundarySpec   `json:"boundary"`
	BoundaryErrorNS     int64          `json:"boundary_error_ns"`
	Corrupt             TruncationInfo `json:"corrupt_sample"`
	CorruptError        string         `json:"corrupt_error"`
	InvalidMetadata     InvalidInfo    `json:"invalid_metadata_sample"`
	RecoveryLog         []string       `json:"recovery_log"`
	Warnings            []string       `json:"warnings,omitempty"`
	ExitCode            int            `json:"exit_code"`
}

// InvalidInfo records a metadata sample that parses but violates normalisation.
type InvalidInfo struct {
	File     string `json:"file"`
	Error    string `json:"error"`
	Kind     string `json:"kind"`
	ExitCode int    `json:"exit_code"`
}

// Result carries the summary and the process exit code. Err is set when the
// run must fail; recovery success keeps the exit code at zero while the
// deliberately corrupt sample's non-zero code stays in the summary.
type Result struct {
	Summary  *Summary
	ExitCode int
	Err      error
}

// trackView is the subset of a composed track the transport scenarios need.
type trackView struct {
	ID         string
	Resolution *normalize.Resolution
}

func viewOf(t *timeline.Track) trackView {
	return trackView{ID: t.ID, Resolution: t.Resolution}
}
