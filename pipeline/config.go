package pipeline

import (
	"fmt"

	"mediapack/model"
	"mediapack/report"
	"mediapack/stream"
)

// Config is the validated demo/run configuration.
type Config struct {
	Seed          int64  `json:"seed"`
	BitrateCount  int    `json:"bitrates"`
	QueueCapacity int    `json:"queue_capacity"`
	OutputDir     string `json:"output_dir"`
	Corrupt       string `json:"corrupt,omitempty"`
	Losses        []int  `json:"losses,omitempty"`
	Reorder       int    `json:"reorder,omitempty"`
	Resume        bool   `json:"resume,omitempty"`
	ReportFormat  string `json:"report_format"`
}

// ResumeInfo reports verification and continuation from an existing manifest.
type ResumeInfo struct {
	Attempted         bool   `json:"attempted"`
	ExistingManifest  bool   `json:"existing_manifest"`
	VerifiedSegments  int    `json:"resumed_segment_count"`
	ContinuedSegments int    `json:"continued_segment_count"`
	ManifestSHA256    string `json:"manifest_sha256"`
}

// CorruptionInfo is the structured offset/category evidence for damaged input.
type CorruptionInfo struct {
	Mode      string              `json:"mode"`
	Offset    int                 `json:"truncate_offset,omitempty"`
	BoxOffset int64               `json:"box_offset"`
	Category  model.ErrorCategory `json:"error_category"`
	Message   string              `json:"message"`
	Recovered bool                `json:"recovered"`
}

// Result is the terminal JSON report.
type Result struct {
	Status         string                 `json:"status"`
	OK             bool                   `json:"ok"`
	ExitCode       int                    `json:"exit_code,omitempty"`
	Command        string                 `json:"command"`
	Config         Config                 `json:"config"`
	OutputDir      string                 `json:"output_dir"`
	ManifestPath   string                 `json:"manifest_path,omitempty"`
	ManifestSHA256 string                 `json:"manifest_sha256,omitempty"`
	GraphOrder     []model.TrackID        `json:"graph_order"`
	Resume         *ResumeInfo            `json:"resume,omitempty"`
	Corruption     *CorruptionInfo        `json:"corruption,omitempty"`
	Metrics        report.Metrics         `json:"metrics"`
	Warnings       []model.Warning        `json:"warnings,omitempty"`
	Recovery       []stream.TrackRecovery `json:"recovery,omitempty"`
}

// Validate checks terminal parameters before any file or CPU work.
func (c Config) Validate() error {
	if c.Seed < 0 {
		return model.NewError("", "", -1, model.CategoryInvalidArgument, "seed must be non-negative", nil)
	}
	if c.BitrateCount < 2 || c.BitrateCount > 4 {
		return model.NewError("", "", -1, model.CategoryInvalidArgument, "bitrates must be a representation count between 2 and 4", nil)
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 256 {
		return model.NewError("", "", -1, model.CategoryInvalidArgument, "queue-capacity must be between 1 and 256", nil)
	}
	if c.OutputDir == "" {
		return model.NewError("", "", -1, model.CategoryInvalidArgument, "output directory is required", nil)
	}
	if c.Reorder < 0 || c.Reorder > 16 {
		return model.NewError("", "", -1, model.CategoryInvalidArgument, "reorder must be between 0 and 16", nil)
	}
	if c.ReportFormat != "json" && c.ReportFormat != "text" {
		return model.NewError("", "", -1, model.CategoryInvalidArgument, "report must be json or text", nil)
	}
	switch c.Corrupt {
	case "", "truncate", "invalid-length", "missing-keyframe", "crc":
	default:
		return model.NewError("", "", -1, model.CategoryInvalidArgument,
			fmt.Sprintf("unsupported corruption mode %q", c.Corrupt), nil)
	}
	for _, loss := range c.Losses {
		if loss < 0 || loss >= 48 {
			return model.NewError("", "", -1, model.CategoryInvalidArgument, "loss sequence must be in [0,47]", nil)
		}
	}
	return nil
}

// ExitCodeFor maps a pipeline error to the process contract.
func ExitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if model.IsCategory(err, model.CategoryInvalidArgument) {
		return 2
	}
	if model.IsCategory(err, model.CategoryChecksumMismatch) || model.IsCategory(err, model.CategoryUnsafeResume) {
		return 3
	}
	return 1
}
