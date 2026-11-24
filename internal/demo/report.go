// Package demo orchestrates the deterministic terminal acceptance scenario:
// healthy/network-recovered media, a corrupt sample, and WAL interruption
// recovery.
package demo

import (
	"medpipe/internal/fault"
	medrtp "medpipe/internal/rtp"
	"medpipe/internal/samplegen"
	"medpipe/internal/txn"
)

// Config controls a demo run.
type Config struct {
	OutDir      string
	Seed        int64
	Faults      []string
	Resume      bool
	LatencyRuns int
}

// SampleReport describes one generated acceptance sample.
type SampleReport struct {
	Kind            string                  `json:"kind"`
	InputPath       string                  `json:"input_path,omitempty"`
	OutputPath      string                  `json:"output_path,omitempty"`
	OutputSHA256    string                  `json:"output_sha256,omitempty"`
	PreResumeSHA256 string                  `json:"pre_resume_sha256,omitempty"`
	PSIVersion      int                     `json:"psi_version,omitempty"`
	EventCount      int                     `json:"event_count"`
	GapEvents       int                     `json:"gap_events"`
	ErrorCodes      []string                `json:"error_codes,omitempty"`
	ErrorCode       fault.Code              `json:"error_code,omitempty"`
	ErrorMessage    string                  `json:"error_message,omitempty"`
	ExitCode        int                     `json:"exit_code"`
	RTPStats        *medrtp.Stats           `json:"rtp_stats,omitempty"`
	NetworkStats    *samplegen.NetworkStats `json:"network_stats,omitempty"`
	Recovery        *txn.RecoveryResult     `json:"recovery,omitempty"`
}

// Report is the single JSON object printed by the CLI. All required acceptance
// fields are top-level.
type Report struct {
	SyncErrorMS       float64 `json:"sync_error_ms"`
	LossRecoveryRate  float64 `json:"loss_recovery_rate"`
	P95LatencyMS      float64 `json:"p95_latency_ms"`
	LatencySamples    int     `json:"latency_samples"`
	DroppedDuplicates int     `json:"dropped_duplicates"`
	GapEvents         int     `json:"gap_events"`
	PSIVersion        int     `json:"psi_version"`
	OutputSHA256      string  `json:"output_sha256"`
	RollbackCount     int     `json:"rollback_count"`
	ExitCode          int     `json:"exit_code"`

	OK           bool          `json:"ok"`
	Seed         int64         `json:"seed"`
	Faults       []string      `json:"faults"`
	OutDir       string        `json:"out_dir"`
	Normal       *SampleReport `json:"normal,omitempty"`
	Corrupt      *SampleReport `json:"corrupt,omitempty"`
	Resume       *SampleReport `json:"resume,omitempty"`
	EventDetails []fault.Event `json:"gap_event_details,omitempty"`
}
