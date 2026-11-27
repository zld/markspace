// Package report renders deterministic, machine-readable regression results.
// The canonical JSON is built without map iteration so repeated runs with
// identical inputs produce byte-identical reports and output digests.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"mediakernel/internal/clock"
)

// Report is the fixed-schema quantification of one pipeline run.
type Report struct {
	SchemaVersion   string `json:"schema_version"`
	Frames          int64  `json:"frames"`
	IndexEntries    int    `json:"index_entries"`
	LocatingErrors  int    `json:"locating_errors"`
	Resume          bool   `json:"resume"`
	ResumeMillis    int64  `json:"resume_ms"`
	ReusedStages    int    `json:"reused_stages"`
	InputDigest     string `json:"input_digest"`
	IndexDigest     string `json:"index_digest"`
	OutputSHA256    string `json:"output_sha256"`
	FirstByteOffset int64  `json:"first_byte_offset"`
	Status          string `json:"status"`
}

// SchemaVersion is bumped when the JSON schema changes.
const SchemaVersion = "report-v1"

// Status values for the run outcome.
const (
	StatusOK      = "ok"
	StatusFaulted = "faulted"
)

// Options mirrors the subset of pipeline fields needed for a report.
type Options struct {
	Frames          int64
	IndexEntries    int
	LocatingErrors  int
	Resume          bool
	ResumeMillis    int64
	ReusedStages    int
	InputDigest     string
	IndexDigest     string
	OutputSHA256    string
	FirstByteOffset int64
}

// Build constructs a report from pipeline outcomes.
func Build(o Options) *Report {
	status := StatusOK
	if o.LocatingErrors > 0 {
		status = StatusFaulted
	}
	return &Report{
		SchemaVersion:   SchemaVersion,
		Frames:          o.Frames,
		IndexEntries:    o.IndexEntries,
		LocatingErrors:  o.LocatingErrors,
		Resume:          o.Resume,
		ResumeMillis:    o.ResumeMillis,
		ReusedStages:    o.ReusedStages,
		InputDigest:     o.InputDigest,
		IndexDigest:     o.IndexDigest,
		OutputSHA256:    o.OutputSHA256,
		FirstByteOffset: o.FirstByteOffset,
		Status:          status,
	}
}

// CanonicalJSON serializes the report with a stable field order (struct
// declaration order) and no trailing whitespace surprises.
func (r *Report) CanonicalJSON() ([]byte, error) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, clock.NewError("report", clock.CatCheckpoint,
			"report marshal failed").Wrap(err)
	}
	return append(raw, '\n'), nil
}

// Digest returns the SHA-256 of the canonical JSON, providing an independent
// content hash over the rendered report itself.
func (r *Report) Digest() (string, error) {
	raw, err := r.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// FromError builds a faulted report from a structured locating error so the
// CLI can emit consistent output even when the pipeline aborts early.
func FromError(o Options, err error) *Report {
	o.LocatingErrors = 1
	o.FirstByteOffset = -1
	if ce, ok := clock.AsError(err); ok && ce.HasOffset {
		o.FirstByteOffset = ce.ByteOffset
	}
	return Build(o)
}
