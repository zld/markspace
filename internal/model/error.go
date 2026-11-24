// Package model contains the error model and shared value types shared by
// the container, stream, transport and transaction subsystems.
package model

import (
	"fmt"

	"github.com/local/medialab/internal/timeline"
)

// Code is a stable, machine-readable error code. Codes are part of the
// public summary contract and must not be renumbered.
type Code string

const (
	// CodeTruncated means declared container/frame data extends past the
	// bytes actually available.
	CodeTruncated Code = "E_TRUNCATED"
	// CodeInvalidTimestamp means a timestamp or duration is negative,
	// zero where it must be positive, or breaks the time ordering.
	CodeInvalidTimestamp Code = "E_INVALID_TIMESTAMP"
	// CodeMissingIndex means a required sample/index table is absent or
	// references an out-of-range offset.
	CodeMissingIndex Code = "E_MISSING_INDEX"
	// CodeIncompleteFrame means a frame boundary was started but not
	// finished; such frames never count as valid keyframes.
	CodeIncompleteFrame Code = "E_INCOMPLETE_FRAME"
	// CodeRTPLate means a packet arrived outside the reorder window and
	// is treated as lost.
	CodeRTPLate Code = "E_RTP_LATE"
	// CodeRTPLost means the sequence gap was never filled before the
	// reorder window advanced.
	CodeRTPLost Code = "E_RTP_LOST"
	// CodeUnrecoverable means repair data could not reconstruct the
	// missing packet(s).
	CodeUnrecoverable Code = "E_UNRECOVERABLE"
	// CodeConflict means two containers advertised conflicting metadata;
	// the arbitration result is still valid but flagged.
	CodeConflict Code = "E_METADATA_CONFLICT"
	// CodeChecksum means a stage output failed its checksum assertion.
	CodeChecksum Code = "E_CHECKSUM"
)

// ErrorRecord is one located, reviewable defect. Every record carries the
// byte offset, track id and master-clock time range needed to re-open the
// source file and verify the finding by hand.
type ErrorRecord struct {
	Code   Code            `json:"code"`
	Offset int64           `json:"offset"`
	Track  uint32          `json:"track"`
	Window timeline.Window `json:"window"`
	Detail string          `json:"detail"`
}

// String renders the record for terminal listings.
func (e ErrorRecord) String() string {
	return fmt.Sprintf("%s track=%d offset=%d %s %s",
		e.Code, e.Track, e.Offset, e.Window.String(), e.Detail)
}

// ErrorBook is the append-only collection of findings for one run. It is
// safe for single-goroutine pipeline use and keeps insertion order so
// repeated runs produce byte-identical summaries.
type ErrorBook struct {
	records []ErrorRecord
}

// Add appends a fully located record.
func (b *ErrorBook) Add(rec ErrorRecord) {
	b.records = append(b.records, rec)
}

// Records returns the recorded findings in insertion order.
func (b *ErrorBook) All() []ErrorRecord {
	return b.records
}

// CountByCode tallies records per error code.
func (b *ErrorBook) CountByCode() map[Code]int {
	out := make(map[Code]int)
	for _, r := range b.records {
		out[r.Code]++
	}
	return out
}

// Has reports whether any record with the given code exists.
func (b *ErrorBook) Has(code Code) bool {
	for _, r := range b.records {
		if r.Code == code {
			return true
		}
	}
	return false
}

// MergeWindows collapses the time windows of all records matching code,
// merging adjacent/overlapping ranges, and returns them in start order.
// These are the reviewable damage ranges printed in the summary.
func (b *ErrorBook) MergeWindows(codes ...Code) []timeline.Window {
	want := make(map[Code]bool, len(codes))
	for _, c := range codes {
		want[c] = true
	}
	var windows []timeline.Window
	for _, r := range b.records {
		if !want[r.Code] {
			continue
		}
		merged := false
		for i := range windows {
			if windows[i].Overlaps(r.Window) || windows[i].End == r.Window.Start || r.Window.End == windows[i].Start {
				windows[i] = windows[i].Merge(r.Window)
				merged = true
				break
			}
		}
		if !merged {
			windows = append(windows, r.Window)
		}
	}
	// repeated single-pass merge may leave chains, so loop to fixpoint
	for changed := true; changed; {
		changed = false
		if len(windows) <= 1 {
			break
		}
		next := windows[:1]
		for _, w := range windows[1:] {
			last := &next[len(next)-1]
			if last.Overlaps(w) || last.End == w.Start {
				*last = last.Merge(w)
				changed = true
			} else {
				next = append(next, w)
			}
		}
		windows = next
	}
	return windows
}
