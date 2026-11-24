// Package index defines random-access index records and rebuilds a valid,
// time-sorted index from repaired media intervals.
package index

import (
	"mediasync/internal/clock"
	"mediasync/internal/media"
)

// Record is one random-access entry: it maps a repaired 90 kHz timestamp to
// a byte offset inside the source WAV payload. Records are the unit of
// on-disk indexing and of corruption recovery.
type Record struct {
	ID         int64
	TimeTicks  clock.Tick
	TimeMs     float64
	ByteOffset int64
	FrameIndex int64
	Length     int64 // bytes covered by this entry
	Concealed  bool  // entry sits inside a silence-concealed gap
}

// Validate checks one record against the actual input length and frame
// geometry. It never mutates the record and returns a structured error
// describing why the record must be dropped.
func (rec Record) Validate(inputLength int64, blockAlign int) error {
	if rec.TimeTicks < 0 {
		return clock.ErrorAt(clock.ErrCorrupt, rec.ByteOffset,
			"record %d has negative timestamp %d", rec.ID, rec.TimeTicks)
	}
	if rec.ByteOffset < 0 || rec.FrameIndex < 0 || rec.Length <= 0 {
		return clock.ErrorAt(clock.ErrCorrupt, rec.ByteOffset,
			"record %d has negative/zero geometry: offset=%d frame=%d len=%d",
			rec.ID, rec.ByteOffset, rec.FrameIndex, rec.Length)
	}
	if blockAlign <= 0 || rec.Length%int64(blockAlign) != 0 {
		return clock.ErrorAt(clock.ErrCorrupt, rec.ByteOffset,
			"record %d length %d not a multiple of blockAlign %d",
			rec.ID, rec.Length, blockAlign)
	}
	if rec.ByteOffset+rec.Length > inputLength {
		return clock.ErrorAt(clock.ErrOverflow, rec.ByteOffset,
			"record %d reaches offset %d beyond input length %d",
			rec.ID, rec.ByteOffset+rec.Length, inputLength)
	}
	if want := rec.FrameIndex * int64(blockAlign); want != rec.ByteOffset {
		return clock.ErrorAt(clock.ErrCorrupt, rec.ByteOffset,
			"record %d frame index %d inconsistent with byte offset", rec.ID, rec.FrameIndex)
	}
	return nil
}

// DropReport explains why an existing index could not be reused wholesale.
type DropReport struct {
	OutOfRange   int
	Duplicate    int
	Corrupt      int
	TotalDropped int
}

// Limit is the hard cap on index records for one input.
const Limit = media.MaxIndexRecords
