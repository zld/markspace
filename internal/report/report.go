// Package report defines the shared failure taxonomy and the terminal/JSON
// report structures produced by every hlsforge subcommand.
package report

// Code is a stable, machine-readable failure identifier.
type Code string

const (
	CodeArgumentError   Code = "ARGUMENT_ERROR"
	CodeFileError       Code = "FILE_ERROR"
	CodeBoxOverflow     Code = "BOX_OVERFLOW"
	CodeTruncatedTable  Code = "TRUNCATED_SAMPLE_TABLE"
	CodeZeroDuration    Code = "ZERO_DURATION_SAMPLE"
	CodeIntegerOverflow Code = "INTEGER_OVERFLOW"
	CodeDuplicate       Code = "DUPLICATE_FRAGMENT"
	CodeConflict        Code = "FRAGMENT_CONFLICT"
	CodeMissingPart     Code = "MISSING_PART"
	CodeTimelineDrift   Code = "TIMELINE_DRIFT"
	CodeLateOutOfWindow Code = "LATE_OUT_OF_WINDOW"
	CodeHashMismatch    Code = "HASH_MISMATCH"
	CodeMissingFile     Code = "MISSING_FILE"
	CodeManifestDiff    Code = "MANIFEST_MISMATCH"
)

// Finding describes one concrete failure, pinned to a bitrate and, when
// applicable, a fragment/part sequence number.
type Finding struct {
	Code    Code   `json:"code"`
	Bitrate int    `json:"bitrate"`
	Seq     int    `json:"seq,omitempty"`
	Part    int    `json:"part,omitempty"`
	Field   string `json:"field,omitempty"`
	Detail  string `json:"detail"`
	// Consistency marks a finding produced by the verifier that must drive
	// exit code 2. Build/demo-time findings are informational even when they
	// reuse a consistency code, because the gaps were intentionally retained.
	Consistency bool `json:"consistency,omitempty"`
}

// TrackSummary carries the per-bitrate counters shown in every report.
type TrackSummary struct {
	BitrateKbps int     `json:"bitrate_kbps"`
	Fragments   int     `json:"fragments"`
	Segments    int     `json:"segments"`
	Parts       int     `json:"parts"`
	DurationS   float64 `json:"duration_s"`
	DeviationMs int64   `json:"deviation_ms"`
	Failures    int     `json:"failures"`
}

// Report is the full result of a demo/build/verify invocation.
type Report struct {
	Command  string         `json:"command"`
	Tracks   []TrackSummary `json:"tracks"`
	Findings []Finding      `json:"findings"`
}

// AddFinding appends a finding and returns it for chaining.
func (r *Report) AddFinding(f Finding) {
	r.Findings = append(r.Findings, f)
}

// HasConsistencyFailure reports whether a finding represents a content
// consistency problem (exit code 2) rather than a usage/IO error. Only
// verifier findings carry the Consistency marker.
func (r *Report) HasConsistencyFailure() bool {
	for _, f := range r.Findings {
		if f.Consistency {
			return true
		}
	}
	return false
}

// MarkConsistency flags every finding as a verifier consistency failure.
func (r *Report) MarkConsistency() {
	for i := range r.Findings {
		r.Findings[i].Consistency = true
	}
}
