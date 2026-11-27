// Package rng reads complete indexed blocks over a half-open nanosecond range
// [start, end). It reports the actual boundaries served and, on failure,
// why a range could not be satisfied, without copying or rewriting media.
package rng

import (
	"fmt"

	"tracecheck/internal/index"
	"tracecheck/internal/serr"
)

// Fragment is one complete block selected by a range read.
type Fragment struct {
	Track       uint64
	BlockIndex  int
	TimeNanos   int64
	SampleStart int64
	SampleEnd   int64
	Keyframe    bool
	Bytes       []byte // the exact original frame bytes
	Hash        string // stable per-fragment digest
}

// Result is the outcome of a range read. Start/End echo the requested range;
// ActualStart/ActualEnd are the media times of the first and last fragments
// returned (equal to the request when the range is exactly covered).
type Result struct {
	RequestedStart int64
	RequestedEnd   int64
	ActualStart    int64
	ActualEnd      int64
	Fragments      []Fragment
	Truncated      bool   // no block exists at or beyond the requested start
	Reason         string // populated on any non-exact boundary
}

// SegmentHash returns the digest of the concatenated fragment bytes in read
// order. An empty result hashes to a fixed digest of no bytes.
func (r *Result) SegmentHash() string {
	h := make([]byte, 0, 4096)
	for _, f := range r.Fragments {
		h = append(h, f.Bytes...)
	}
	return index.HashBytes(h)
}

// Read returns every fragment whose media time lies in [start, end). start must
// be less than end. When the requested span touches no block the result is
// empty and Reason explains why; a range that starts at or beyond the last
// indexed time returns RANGE_OUT_OF_BOUNDS.
func Read(ix *index.Index, data []byte, start, end int64) (*Result, error) {
	if start >= end {
		return nil, serr.New(serr.CodeInvalidArgs,
			"range start %d must be before end %d", start, end)
	}
	if ix == nil {
		return nil, serr.New(serr.CodeInvalidArgs, "nil index")
	}
	lo, hi, err := ix.Span()
	if err != nil {
		return nil, err
	}
	res := &Result{RequestedStart: start, RequestedEnd: end}
	if start >= hi {
		return nil, serr.New(serr.CodeRangeOutOfBounds,
			"range start %d is at or past the last indexed time %d", start, hi).
			WithPath([]string{"range"})
	}
	if end <= lo {
		res.Truncated = true
		res.Reason = fmt.Sprintf("range ends at %d before the first indexed time %d", end, lo)
		return res, nil
	}
	for _, ti := range ix.Tracks {
		for _, rec := range ti.Records {
			if rec.TimeNanos < start || rec.TimeNanos >= end {
				continue
			}
			if rec.SampleStart < 0 || rec.SampleEnd > int64(len(data)) ||
				rec.SampleStart > rec.SampleEnd {
				return nil, serr.New(serr.CodeRangeOutOfBounds,
					"fragment for track %d block %d has bytes [%d,%d) outside the %d byte input",
					rec.Track, rec.BlockIndex, rec.SampleStart, rec.SampleEnd, len(data)).
					At(rec.SampleStart).WithPath([]string{"range"})
			}
			body := data[rec.SampleStart:rec.SampleEnd]
			res.Fragments = append(res.Fragments, Fragment{
				Track:       rec.Track,
				BlockIndex:  rec.BlockIndex,
				TimeNanos:   rec.TimeNanos,
				SampleStart: rec.SampleStart,
				SampleEnd:   rec.SampleEnd,
				Keyframe:    rec.Keyframe,
				Bytes:       body,
				Hash:        index.HashBytes(body),
			})
		}
	}
	if len(res.Fragments) == 0 {
		res.Truncated = true
		res.Reason = fmt.Sprintf("no block starts within [%d,%d)", start, end)
		return res, nil
	}
	sortFragments(res.Fragments)
	res.ActualStart = res.Fragments[0].TimeNanos
	res.ActualEnd = res.Fragments[len(res.Fragments)-1].TimeNanos
	if res.ActualStart > start {
		res.Reason = fmt.Sprintf("first block at %d is %d ns after the requested start",
			res.ActualStart, res.ActualStart-start)
	} else if res.ActualEnd < end-1 {
		res.Reason = fmt.Sprintf("last block at %d precedes the requested end %d",
			res.ActualEnd, end)
	}
	return res, nil
}

// sortFragments orders by media time, then track, then block index; the same
// key used to build the index.
func sortFragments(frags []Fragment) {
	// Insertion sort keeps the stable order deterministic for the small
	// per-range fragment counts and avoids a sort import for one call.
	for i := 1; i < len(frags); i++ {
		for j := i; j > 0; j-- {
			if lessFrag(frags[j], frags[j-1]) {
				frags[j], frags[j-1] = frags[j-1], frags[j]
				continue
			}
			break
		}
	}
}

func lessFrag(a, b Fragment) bool {
	if a.TimeNanos != b.TimeNanos {
		return a.TimeNanos < b.TimeNanos
	}
	if a.Track != b.Track {
		return a.Track < b.Track
	}
	return a.BlockIndex < b.BlockIndex
}
