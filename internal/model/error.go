package model

import "fmt"

// ErrorKind enumerates the classified failure modes understood by the
// pipeline. Classifications drive the per-item conclusion rather than panic
// paths, so a corrupt representation never takes the others down.
type ErrorKind string

const (
	KindManifestSyntax     ErrorKind = "manifest_syntax"      // unparsable tag / malformed manifest
	KindManifestTruncated  ErrorKind = "manifest_truncated"   // manifest ends before all referenced media closes
	KindMissingDuration    ErrorKind = "missing_duration"     // a segment / representation declares no duration
	KindDuplicateReference ErrorKind = "duplicate_reference"  // the same media URL is referenced twice
	KindRangeOutOfBounds   ErrorKind = "range_out_of_bounds"   // declared time range exceeds the representation span
	KindSegmentCorrupt     ErrorKind = "segment_corrupt"       // fragment cannot be demuxed
	KindTimestampWrap      ErrorKind = "timestamp_wraparound"  // timestamp wraparound was expanded (informational)
	KindDriftExceeded      ErrorKind = "drift_exceeded"        // estimated drift is above the configured threshold
	KindBoundaryMismatch   ErrorKind = "boundary_mismatch"     // shared-timeline boundary cross-check failed
	KindTrackMissing       ErrorKind = "track_missing"         // required output track has no source track
	KindTrackDuplicate     ErrorKind = "track_duplicate"       // one source track is mapped to several outputs
	KindTimescaleIncompat  ErrorKind = "timescale_incompatible" // mapped tracks use incompatible time bases
	KindRemuxMismatch      ErrorKind = "remux_fingerprint_mismatch" // lossless remux changed sample bytes
	KindUntrustedBoundary  ErrorKind = "untrusted_boundary"   // boundary cannot be trusted, manual review needed
)

// MediaError is the shared structured error model. Every error that can be
// attributed to input carries the source file plus an offset, or a manifest
// line number.
type MediaError struct {
	Kind     ErrorKind
	File     string // source file (manifest, segment, playlist)
	Offset   int64  // byte offset inside a binary segment, -1 when not applicable
	Line     int    // 1-based manifest/playlist line, 0 when not applicable
	Item     string // representation id / variant name the error belongs to
	Message  string
}

// Error implements the error interface.
func (e *MediaError) Error() string {
	switch {
	case e.Line > 0:
		return fmt.Sprintf("%s: %s:%d: %s", e.Kind, e.File, e.Line, e.Message)
	case e.Offset >= 0:
		return fmt.Sprintf("%s: %s@offset %d: %s", e.Kind, e.File, e.Offset, e.Message)
	default:
		return fmt.Sprintf("%s: %s: %s", e.Kind, e.File, e.Message)
	}
}

// New builds a MediaError without location information.
func New(kind ErrorKind, file, msg string) *MediaError {
	return &MediaError{Kind: kind, File: file, Offset: -1, Line: 0, Message: msg}
}

// AtLine attaches a manifest line number.
func AtLine(kind ErrorKind, file string, line int, msg string) *MediaError {
	return &MediaError{Kind: kind, File: file, Offset: -1, Line: line, Message: msg}
}

// AtOffset attaches a byte offset inside a segment.
func AtOffset(kind ErrorKind, file string, offset int64, item, msg string) *MediaError {
	return &MediaError{Kind: kind, File: file, Offset: offset, Line: 0, Item: item, Message: msg}
}

// ForItem copies the error and attributes it to a representation/variant.
func (e *MediaError) ForItem(item string) *MediaError {
	cp := *e
	cp.Item = item
	return &cp
}
