// Package pipeline wires ingest, normalization, indexing, diagnosis and
// validation into one deterministic run with resumable checkpoints.
package pipeline

import (
	"mediad/internal/clock"
	"mediad/internal/core"
	"mediad/internal/diagnose"
	"mediad/internal/index"
	"mediad/internal/validate"
)

// Parameters are the non-input settings that must be identical for a
// checkpoint to be reusable.
type Parameters struct {
	Seed          int64  `json:"seed"`
	MaxAssetBytes int64  `json:"max_asset_bytes"`
	ClockRate     int64  `json:"clock_rate"`
	MasterTrack   string `json:"master_track"`
}

// AssetSummary is the per-input record emitted before any stage output.
type AssetSummary struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// CueRecord is a normalized subtitle cue for the report.
type CueRecord struct {
	Asset     string        `json:"asset"`
	Format    string        `json:"format"`
	Index     int           `json:"index"`
	ID        string        `json:"id,omitempty"`
	Start     core.Rational `json:"start"`
	End       core.Rational `json:"end"`
	Span      core.Span     `json:"span"`
	Lines     []string      `json:"lines"`
	Precision string        `json:"precision"`
}

// RTPSummary is the per-capture RTP accounting.
type RTPSummary struct {
	Asset      string `json:"asset"`
	Packets    int    `json:"packets"`
	Wraps      int    `json:"wraps"`
	GapLoss    int64  `json:"gap_loss"`
	OutOfOrder int    `json:"out_of_order"`
}

// TimecodeRecord is one parsed SMPTE label.
type TimecodeRecord struct {
	Asset  string        `json:"asset"`
	Raw    string        `json:"raw"`
	Rate   string        `json:"rate"`
	Drop   bool          `json:"drop"`
	Frames int64         `json:"frames"`
	Clock  core.Rational `json:"clock"`
	Span   core.Span     `json:"span"`
}

// DriftRecord is one audited node drift fit.
type DriftRecord struct {
	Asset       string        `json:"asset"`
	Node        string        `json:"node"`
	Samples     int           `json:"samples"`
	Slope       core.Rational `json:"slope"`
	Intercept   core.Rational `json:"intercept"`
	RatePPB     int64         `json:"rate_ppb"`
	MaxResidual core.Rational `json:"max_residual"`
	Applied     bool          `json:"applied"`
	Span        core.Span     `json:"span"`
}

// IngestStage is the parse stage output.
type IngestStage struct {
	Assets    []AssetSummary   `json:"assets"`
	Cues      []CueRecord      `json:"cues"`
	RTP       []RTPSummary     `json:"rtp"`
	Timecodes []TimecodeRecord `json:"timecodes"`
	Drifts    []DriftRecord    `json:"drifts"`
	Issues    diagnose.Issues  `json:"issues"`
}

// NormalizeStage is the shared-clock stage output.
type NormalizeStage struct {
	Tracks []clock.Track   `json:"tracks"`
	Points []clock.Point   `json:"points"`
	Wraps  int             `json:"wraps"`
	Clamps int             `json:"clamps"`
	Issues diagnose.Issues `json:"issues"`
}

// IndexStage is the keyframe-index stage output.
type IndexStage struct {
	Indexes []index.Index   `json:"indexes"`
	Issues  diagnose.Issues `json:"issues"`
}

// DiagnoseStage is the damage-correlation stage output.
type DiagnoseStage struct {
	Ranges []diagnose.Range `json:"ranges"`
	Issues diagnose.Issues  `json:"issues"`
}

// StageCheck records the outcome of checking one stage's own output at the
// point it is produced. It is separate from the final cross-stage validation.
type StageCheck struct {
	Stage  string `json:"stage"`
	Name   string `json:"name"`
	Passed int    `json:"passed"`
	Failed int    `json:"failed"`
}

// ValidateStage is the invariant stage output.
type ValidateStage struct {
	Result validate.Result `json:"result"`
	Issues diagnose.Issues `json:"issues"`
	Gates  []StageCheck    `json:"gates"`
}

// StageDigest is one committed stage in the report journal.
type StageDigest struct {
	Name           string `json:"name"`
	Digest         string `json:"digest"`
	RecordsThrough int    `json:"records_through"`
}

// OffsetCheck compares one reported offset with fixture ground truth.
type OffsetCheck struct {
	Reported int64
	Expected int64
}

// Summary carries the demo counters and the report checksum.
type Summary struct {
	Cues             int    `json:"cues"`
	Frames           int    `json:"frames"`
	KeyframeGaps     int    `json:"keyframe_gaps"`
	CorruptRanges    int    `json:"corrupt_ranges"`
	OffsetErrorBytes int64  `json:"offset_error_bytes"`
	Issues           int    `json:"issues"`
	InvariantsFailed int    `json:"invariants_failed"`
	SHA256           string `json:"sha256"`
}

// Output is the complete result of a run, reusable by the library caller.
type Output struct {
	Assets    []AssetSummary
	Ingest    *IngestStage
	Normalize *NormalizeStage
	Index     *IndexStage
	Diagnose  *DiagnoseStage
	Validate  *ValidateStage
	Summary   Summary
	Stages    []StageDigest
	Resumed   []string
	ReusedAll bool
	Damaged   bool
	// OffsetChecks compares reported offsets with known ground truth so a
	// harness can score localization accuracy.
	InputDigest  string
	ParamsDigest string
	OffsetChecks []OffsetCheck
	// cache carries typed parse results between stages. It is never encoded
	// into a checkpoint or a report.
	cache *parseCache
}
