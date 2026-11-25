// Package pipeline wires generation/ingress, parsing, scanning, timeline,
// diagnostics, export, checkpointing, and manifest archival.
package pipeline

import (
	"github.com/zld/media-check/internal/diagnostic"
	"github.com/zld/media-check/internal/export"
	"github.com/zld/media-check/internal/jobs"
	"github.com/zld/media-check/internal/manifest"
	"github.com/zld/media-check/internal/mp4"
	"github.com/zld/media-check/internal/stream"
	"github.com/zld/media-check/internal/timeline"
)

type Config struct {
	Seed           int64
	Corrupt        string
	WorkDir        string
	TaskKey        string
	InterruptAfter string
	Resume         bool
}

// State is serialized inside a checkpoint. It contains no raw media bytes;
// bytes are re-read from the work-local input after fingerprint verification.
type State struct {
	Config       Config               `json:"config"`
	TaskKey      string               `json:"task_key"`
	InputRelPath string               `json:"input_rel_path"`
	Input        manifest.InputRecord `json:"input"`
	MP4          *mp4.Manifest        `json:"mp4,omitempty"`
	Parse        *mp4.ParseResult     `json:"parse,omitempty"`
	Scan         *stream.Result       `json:"scan,omitempty"`
	Timeline     *timeline.Report     `json:"timeline,omitempty"`
	Diagnostic   *diagnostic.Report   `json:"diagnostic,omitempty"`
	Export       *export.Result       `json:"export,omitempty"`
	Manifest     *manifest.Manifest   `json:"manifest,omitempty"`
	Stages       []string             `json:"stages"`
}

type Outcome struct {
	ExitCode       int
	TaskKey        string
	WorkDir        string
	Interrupted    bool
	Resumed        bool
	ResumeFrom     string
	Damage         *diagnostic.Report
	Manifest       *manifest.Manifest
	ManifestPath   string
	ArchivePath    string
	ManifestSHA256 string
	Job            *jobs.JobRecord
}

const (
	ExitOK     = 0
	ExitDamage = 2
	ExitState  = 4
	ExitUsage  = 64
)
