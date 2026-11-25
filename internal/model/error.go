// Package model defines the error record and shared data structures used by
// the metadata, RTP recovery and transaction subsystems. An ErrorRecord
// locates every fault in a reviewable interval: byte offset, owning track
// and master-tick time range, plus a stable machine-readable code.
package model

import (
	"fmt"
	"strings"
)

// ErrorCode is a stable identifier for a fault class.
type ErrorCode string

const (
	// ErrTruncatedHeader: a container header ends before its declared size.
	ErrTruncatedHeader ErrorCode = "TRUNCATED_HEADER"
	// ErrInvalidTimestamp: a time stamp is negative, zero-rate or out of range.
	ErrInvalidTimestamp ErrorCode = "INVALID_TIMESTAMP"
	// ErrCorruptPacket: an RTP packet fails parsing or violates framing.
	ErrCorruptPacket ErrorCode = "CORRUPT_PACKET"
	// ErrMetadataConflict: sources disagree on a metadata field.
	ErrMetadataConflict ErrorCode = "METADATA_CONFLICT"
	// ErrMissingPacket: a sequence gap was not filled inside the wait window.
	ErrMissingPacket ErrorCode = "MISSING_PACKET"
	// ErrAlignmentExceeded: audio/video drift is above the configured limit.
	ErrAlignmentExceeded ErrorCode = "ALIGNMENT_EXCEEDED"
	// ErrTxnIncomplete: a stage was not committed and had to be rolled back.
	ErrTxnIncomplete ErrorCode = "TXN_INCOMPLETE"
	// ErrLimitExceeded: a configured memory/window/output cap was reached.
	ErrLimitExceeded ErrorCode = "LIMIT_EXCEEDED"
)

// Severity ranks how a record affects the pipeline.
type Severity string

const (
	// SevWarn lets the pipeline continue (e.g. a recovered conflict).
	SevWarn Severity = "warn"
	// SevError makes the current stage fail.
	SevError Severity = "error"
)

// ErrorRecord is one locatable fault. Offset is a byte position inside the
// input stream; StartTick/EndTick are the affected interval on the master
// clock (clock package). A negative offset means "not byte-addressable".
type ErrorRecord struct {
	Code      ErrorCode `json:"code"`
	Severity  Severity  `json:"severity"`
	Track     string    `json:"track"`
	Offset    int64     `json:"offset"`
	StartTick int64     `json:"start_tick"`
	EndTick   int64     `json:"end_tick"`
	Message   string    `json:"message"`
}

// String renders a record in a stable, greppable single-line form.
func (e ErrorRecord) String() string {
	return fmt.Sprintf("%s %s track=%s offset=%d ticks=[%d,%d] %s",
		e.Severity, e.Code, e.Track, e.Offset, e.StartTick, e.EndTick, e.Message)
}

// Recorder collects records and tracks whether any error-severity entry was
// seen. It is safe for single-goroutine pipeline stages and is shared by at
// least the metadata and RTP recovery subsystems.
type Recorder struct {
	records []ErrorRecord
	errSeen bool
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Add appends a record and flips the error flag when severity is error.
func (r *Recorder) Add(rec ErrorRecord) {
	if rec.Severity == "" {
		rec.Severity = SevError
	}
	r.records = append(r.records, rec)
	if rec.Severity == SevError {
		r.errSeen = true
	}
}

// Records returns a copy of all collected records.
func (r *Recorder) Records() []ErrorRecord {
	out := make([]ErrorRecord, len(r.records))
	copy(out, r.records)
	return out
}

// HasError reports whether any error-severity record was collected.
func (r *Recorder) HasError() bool { return r.errSeen }

// CountByCode returns how many records exist for each code.
func (r *Recorder) CountByCode() map[ErrorCode]int {
	m := make(map[ErrorCode]int)
	for _, rec := range r.records {
		m[rec.Code]++
	}
	return m
}

// JoinMessages joins the messages of all error-severity records. It is used
// to build a returned Go error for stage failure.
func (r *Recorder) JoinMessages() string {
	var parts []string
	for _, rec := range r.records {
		if rec.Severity == SevError {
			parts = append(parts, rec.String())
		}
	}
	return strings.Join(parts, "; ")
}

// Reset empties the recorder. Used by tests that reuse an instance.
func (r *Recorder) Reset() {
	r.records = nil
	r.errSeen = false
}
