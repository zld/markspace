// Package media contains the shared media clock, frame record format and
// serializable error model used by every stage of the processing kernel.
package media

import (
	"errors"
	"fmt"
)

// Category identifies a stable, serializable failure class. Callers may rely
// on these values when deciding whether a failure is recoverable by fixing the
// input material and rerunning the pipeline.
type Category string

const (
	// Frame record problems.
	CatBadMagic       Category = "bad_magic"
	CatTruncatedFrame Category = "truncated_frame"
	CatSizeLimit      Category = "size_limit"
	CatChecksum       Category = "checksum_mismatch"
	CatMetaCorrupt    Category = "metadata_corrupt"
	CatTimestampBack  Category = "timestamp_regression"

	// Ingest problems.
	CatDuplicatePacket Category = "duplicate_packet"
	CatLatePacket      Category = "late_packet"
	CatPacketGap       Category = "packet_gap"
	CatMalformedPacket Category = "malformed_packet"

	// Index problems.
	CatIndexDigest    Category = "index_digest_mismatch"
	CatIndexVersion   Category = "index_version_mismatch"
	CatIndexTruncated Category = "index_truncated"
	CatIndexTemp      Category = "index_temp_residue"
	CatIndexInput     Category = "index_input_changed"

	// Manifest / processing problems.
	CatManifestMismatch  Category = "manifest_mismatch"
	CatTimeDiscontinuity Category = "time_discontinuity"

	// Infrastructure problems.
	CatInvalidParams Category = "invalid_params"
	CatIO            Category = "io_error"
	CatPathEscape    Category = "path_escape"
	CatUnknown       Category = "unknown"
)

// Stage names the pipeline phase where an error was observed.
type Stage string

const (
	StageIngest      Stage = "ingest"
	StageScan        Stage = "scan"
	StageIndex       Stage = "index"
	StageSegment     Stage = "segment"
	StageSilence     Stage = "silence"
	StageFingerprint Stage = "fingerprint"
	StageManifest    Stage = "manifest"
	StageVerify      Stage = "verify"
	StageCLI         Stage = "cli"
)

// Error is a fully serializable processing failure. Every stage returns this
// type so a terminal user can locate the offending record and understand how
// to recover without re-reading source code.
type Error struct {
	Category    Category `json:"category"`
	Stage       Stage    `json:"stage"`
	Message     string   `json:"message"`
	FrameOffset int64    `json:"frame_offset,omitempty"`
	Seq         uint32   `json:"seq,omitempty"`
	Recoverable bool     `json:"recoverable"`
	Suggestion  string   `json:"suggestion,omitempty"`

	cause error
}

// Error implements the error interface.
func (e *Error) Error() string {
	loc := ""
	if e.FrameOffset > 0 || e.Category == CatTruncatedFrame {
		loc = fmt.Sprintf(" at frame_offset=%d", e.FrameOffset)
	}
	if e.Seq != 0 {
		loc += fmt.Sprintf(" seq=%d", e.Seq)
	}
	return fmt.Sprintf("%s/%s: %s%s", e.Stage, e.Category, e.Message, loc)
}

// Unwrap exposes the underlying cause when one was attached.
func (e *Error) Unwrap() error { return e.cause }

// New builds a recoverable error with the standard suggestion for its category.
func New(cat Category, stage Stage, format string, args ...any) *Error {
	return &Error{
		Category:    cat,
		Stage:       stage,
		Message:     fmt.Sprintf(format, args...),
		Recoverable: isRecoverable(cat),
		Suggestion:  suggestion(cat),
	}
}

// Wrap attaches an underlying error to a categorized one.
func Wrap(cat Category, stage Stage, cause error, format string, args ...any) *Error {
	e := New(cat, stage, format, args...)
	e.cause = cause
	return e
}

// WithOffset records the byte offset of the offending record.
func (e *Error) WithOffset(off int64) *Error {
	e.FrameOffset = off
	return e
}

// WithSeq records the frame or packet sequence number.
func (e *Error) WithSeq(seq uint32) *Error {
	e.Seq = seq
	return e
}

// AsError extracts a *Error from err, if present.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// CategoryOf returns the category of err or CatUnknown.
func CategoryOf(err error) Category {
	if e, ok := AsError(err); ok {
		return e.Category
	}
	if err == nil {
		return ""
	}
	return CatUnknown
}

func isRecoverable(cat Category) bool {
	switch cat {
	case CatTruncatedFrame, CatChecksum, CatMetaCorrupt, CatTimestampBack,
		CatIndexDigest, CatIndexVersion, CatIndexTruncated, CatIndexInput,
		CatManifestMismatch, CatTimeDiscontinuity, CatPacketGap:
		return true
	default:
		return false
	}
}

func suggestion(cat Category) string {
	switch cat {
	case CatTruncatedFrame:
		return "restore the complete frame payload or drop the trailing partial record, then rescan"
	case CatChecksum:
		return "repair the damaged bytes from the capture source; the frame cannot be trusted"
	case CatSizeLimit:
		return "reject the oversized record or raise the configured limits deliberately"
	case CatBadMagic:
		return "point the scanner at a mediacore frame stream (magic MFR1)"
	case CatMetaCorrupt:
		return "treat the metadata-dependent interval as unknown and repair/recapture it"
	case CatDuplicatePacket:
		return "duplicate suppressed; no action required"
	case CatLatePacket:
		return "the packet arrived after its reorder window closed; the affected frame is reported as a gap"
	case CatPacketGap:
		return "continue processing; the gap is explicit in the index and manifest"
	case CatIndexDigest, CatIndexInput:
		return "rebuild the index from the current input with the index cache"
	case CatIndexVersion:
		return "rebuild the index with the current index version"
	case CatIndexTruncated:
		return "discard the incomplete cache file and rebuild atomically"
	case CatManifestMismatch:
		return "rebuild the manifest from the current index; recompute totals from entries"
	case CatInvalidParams:
		return "correct the command line parameters"
	case CatPathEscape:
		return "choose an output directory and keep all writes inside it"
	default:
		return ""
	}
}
