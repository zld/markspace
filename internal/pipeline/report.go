// Package pipeline wires the scanning, RTP recovery, synchronisation, quality,
// manifest and archive stages into one reproducible run.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"mediacore/internal/bitstream"
	"mediacore/internal/media"
)

// Options configures one demo run.
type Options struct {
	Dir          string
	Codec        bitstream.Codec
	Seed         int64
	Loss         int
	Reorder      int
	BudgetMS     int64
	QualityMin   float64
	TargetKbps   float64
	CorruptIndex bool
	Interrupt    bool
	Resume       bool
	Preset       string
	Width        int
	Height       int
	FPS          int
	Seconds      int
	WaitWindow   int
	BufferLimit  int
}

// Defaults returns the documented demo parameters.
func Defaults() Options {
	return Options{
		Codec:       bitstream.CodecAV1,
		Seed:        17,
		Width:       640,
		Height:      360,
		FPS:         30,
		Seconds:     4,
		BudgetMS:    5000,
		QualityMin:  30,
		TargetKbps:  900,
		WaitWindow:  8,
		BufferLimit: 64 * 1024,
	}
}

// Report is the machine-readable record of a run. Digest is stable for
// identical inputs and parameters; environment-dependent fields are excluded
// from it deliberately.
type Report struct {
	Codec             string            `json:"codec"`
	Seed              int64             `json:"seed"`
	Decision          string            `json:"decision"`
	DecisionReason    string            `json:"decision_reason"`
	TargetKbps        float64           `json:"target_kbps"`
	ActualKbps        float64           `json:"actual_kbps"`
	DeviationPct      float64           `json:"deviation_pct"`
	PSNRdB            float64           `json:"psnr_db"`
	QualityMin        float64           `json:"quality_min_db"`
	BudgetMS          int64             `json:"budget_ms"`
	ProcessMS         int64             `json:"process_ms"`
	RecoveredPackets  int               `json:"recovered_packets"`
	RecoveredExpected int               `json:"recovered_expected"`
	LostPackets       int               `json:"lost_packets"`
	MissingPackets    int               `json:"missing_packets"`
	Packets           int               `json:"packets"`
	SyncOffsetMS      float64           `json:"sync_offset_ms"`
	SyncResidualMS    float64           `json:"sync_residual_ms"`
	ManifestTimeErrMS float64           `json:"manifest_time_error_ms"`
	ManifestEntries   int               `json:"manifest_entries"`
	ScanFrames        int               `json:"scan_frames"`
	Keyframes         int               `json:"keyframes"`
	IndexQuarantined  int               `json:"index_quarantined"`
	ArchiveDigest     string            `json:"archive_digest"`
	ArchiveDuplicate  bool              `json:"archive_duplicate"`
	Container         media.ProbeResult `json:"container"`
	ContainerDriftMS  float64           `json:"container_drift_ms"`
	MigrationStage    string            `json:"migration_stage"`
	MigrationResumed  bool              `json:"migration_resumed"`
	MigrationNoop     bool              `json:"migration_noop"`
	ManifestDigest    string            `json:"manifest_digest"`
	FineTunePasses    int               `json:"fine_tune_passes"`
	CorruptIndex      bool              `json:"corrupt_index"`
	Digest            string            `json:"digest"`
}

// stable is the subset of the report covered by Digest. Timing, resume and
// idempotency flags depend on the filesystem state or wall clock, not on the
// logical result, so they are excluded.
type stable struct {
	Codec             string  `json:"codec"`
	Seed              int64   `json:"seed"`
	Decision          string  `json:"decision"`
	DecisionReason    string  `json:"decision_reason"`
	TargetKbps        float64 `json:"target_kbps"`
	ActualKbps        float64 `json:"actual_kbps"`
	DeviationPct      float64 `json:"deviation_pct"`
	PSNRdB            float64 `json:"psnr_db"`
	QualityMin        float64 `json:"quality_min_db"`
	BudgetMS          int64   `json:"budget_ms"`
	RecoveredPackets  int     `json:"recovered_packets"`
	RecoveredExpected int     `json:"recovered_expected"`
	LostPackets       int     `json:"lost_packets"`
	MissingPackets    int     `json:"missing_packets"`
	SyncOffsetMS      float64 `json:"sync_offset_ms"`
	SyncResidualMS    float64 `json:"sync_residual_ms"`
	ManifestTimeErrMS float64 `json:"manifest_time_error_ms"`
	ManifestEntries   int     `json:"manifest_entries"`
	ManifestDigest    string  `json:"manifest_digest"`
	ScanFrames        int     `json:"scan_frames"`
	Keyframes         int     `json:"keyframes"`
	ArchiveDigest     string  `json:"archive_digest"`
	FineTunePasses    int     `json:"fine_tune_passes"`
}

func (r *Report) computeDigest() {
	s := stable{
		Codec: r.Codec, Seed: r.Seed, Decision: r.Decision, DecisionReason: r.DecisionReason,
		TargetKbps: r.TargetKbps, ActualKbps: r.ActualKbps, DeviationPct: r.DeviationPct,
		PSNRdB: r.PSNRdB, QualityMin: r.QualityMin, BudgetMS: r.BudgetMS,
		RecoveredPackets: r.RecoveredPackets, RecoveredExpected: r.RecoveredExpected,
		LostPackets: r.LostPackets, MissingPackets: r.MissingPackets,
		SyncOffsetMS: r.SyncOffsetMS, SyncResidualMS: r.SyncResidualMS,
		ManifestTimeErrMS: r.ManifestTimeErrMS, ManifestEntries: r.ManifestEntries,
		ManifestDigest: r.ManifestDigest, ScanFrames: r.ScanFrames, Keyframes: r.Keyframes,
		ArchiveDigest: r.ArchiveDigest, FineTunePasses: r.FineTunePasses,
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	sum := sha256.Sum256(b)
	r.Digest = hex.EncodeToString(sum[:])
}

// Run executes the full demo. A rejected input returns the partial report with
// the matching fault; a suspended migration returns exit status 3.
