// Package mpierr defines the structured error model used across the kernel.
//
// Every processing error carries the RTP extended sequence number where the
// problem starts, a byte offset measured from the beginning of the input
// stream (so a corrupted byte can be located to within 1 byte), an error
// category and the dependency-graph node that observed it.
package mpierr

import "fmt"

// Category classifies an error so recovery policy can branch on it.
type Category uint8

const (
	CatUnknown Category = iota
	// CatGap: one or more sequence numbers are missing. Recoverable by
	// skipping the gap (no audio content is fabricated).
	CatGap
	// CatCorrupt: the packet or its frame failed integrity verification.
	CatCorrupt
	// CatTruncated: the input ended before a complete packet was parsed.
	CatTruncated
	// CatLate: a packet arrived after the jitter window was released.
	CatLate
	// CatDuplicate: a repeated extended sequence number.
	CatDuplicate
	// CatSync: audio/video offset exceeded the configured tolerance.
	CatSync
	// CatDAG: invalid dependency graph (e.g. a cycle or missing node).
	CatDAG
	// CatLimit: an input exceeded a configured hard limit.
	CatLimit
	// CatCheckpoint: checkpoint store failure.
	CatCheckpoint
)

// String maps a category to its stable wire name.
func (c Category) String() string {
	switch c {
	case CatGap:
		return "gap"
	case CatCorrupt:
		return "corrupt"
	case CatTruncated:
		return "truncated"
	case CatLate:
		return "late"
	case CatDuplicate:
		return "duplicate"
	case CatSync:
		return "sync"
	case CatDAG:
		return "dag"
	case CatLimit:
		return "limit"
	case CatCheckpoint:
		return "checkpoint"
	default:
		return "unknown"
	}
}

// MediaError is the structured error type produced by every kernel stage.
type MediaError struct {
	// FirstSeq is the extended sequence number where the problem begins.
	// For a gap it is the first missing sequence number.
	FirstSeq uint64
	// ByteOffset is the stream-relative byte offset of the first offending
	// byte. Unknown is represented by -1.
	ByteOffset int64
	// Category is the machine-readable error class.
	Category Category
	// Node is the dependency-graph node that produced the error ("" if none).
	Node string
	// Message is the human-readable description.
	Message string
	// Cause is the wrapped underlying error, if any.
	Cause error
}

// New builds a MediaError with an unknown byte offset.
func New(cat Category, firstSeq uint64, node, msg string) *MediaError {
	return &MediaError{FirstSeq: firstSeq, ByteOffset: -1, Category: cat, Node: node, Message: msg}
}

// AtOffset attaches a stream-relative byte offset and returns the error.
func (e *MediaError) AtOffset(off int64) *MediaError {
	e.ByteOffset = off
	return e
}

// WithNode attaches the observing node and returns the error.
func (e *MediaError) WithNode(node string) *MediaError {
	e.Node = node
	return e
}

// Wrap attaches an underlying cause and returns the error.
func (e *MediaError) Wrap(cause error) *MediaError {
	e.Cause = cause
	return e
}

// Error implements error.
func (e *MediaError) Error() string {
	s := fmt.Sprintf("%s: seq=%d offset=%d node=%s: %s",
		e.Category, e.FirstSeq, e.ByteOffset, e.Node, e.Message)
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap enables errors.Is/errors.As on the wrapped cause.
func (e *MediaError) Unwrap() error { return e.Cause }
