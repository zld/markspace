// Package rangeread executes precise half-open [start, end) time-range
// reads against the random-access index, returning every intersecting
// sample together with its source offset in the container.
package rangeread

import (
	"io"

	"mediakernel/internal/clock"
	"mediakernel/internal/fault"
	"mediakernel/internal/index"
)

// Request asks for the samples of one track intersecting [Start, End).
type Request struct {
	TrackID uint32
	Start   clock.Rational
	End     clock.Rational
}

// Hit is one intersecting sample and where its bytes live.
type Hit struct {
	Entry index.Entry
}

// Read resolves requests against the index. The same track may not be
// requested twice in one call (ERR_DUPLICATE_TRACK); an empty or inverted
// range is rejected (ERR_EMPTY_RANGE). Samples intersect the range when
// [sampleStart, sampleEnd) overlaps [Start, End).
func Read(ix *index.Index, reqs ...Request) ([][]Hit, error) {
	seen := map[uint32]bool{}
	out := make([][]Hit, 0, len(reqs))
	for _, q := range reqs {
		if seen[q.TrackID] {
			return nil, fault.New(fault.ErrDuplicateTrack, fault.NA,
				"track %d requested twice", q.TrackID).WithTrack(q.TrackID)
		}
		seen[q.TrackID] = true
		if clock.Cmp(q.Start, q.End) >= 0 {
			return nil, fault.New(fault.ErrEmptyRange, fault.NA,
				"range [%d/%d,%d/%d) is empty", q.Start.Num, q.Start.Den, q.End.Num, q.End.Den).
				WithTrack(q.TrackID)
		}
		var hits []Hit
		for _, e := range ix.Track(q.TrackID) {
			if intersects(e, q) {
				hits = append(hits, Hit{Entry: e})
			}
		}
		out = append(out, hits)
	}
	return out, nil
}

func intersects(e index.Entry, q Request) bool {
	// Point-like sample (zero duration): intersects iff Start <= t < End.
	if clock.Cmp(e.Start, e.End) == 0 {
		return clock.Cmp(e.Start, q.Start) >= 0 && clock.Cmp(e.Start, q.End) < 0
	}
	return clock.Cmp(e.Start, q.End) < 0 && clock.Cmp(e.End, q.Start) > 0
}

// ReadData fetches the payload bytes of every hit from the container.
func ReadData(r io.ReaderAt, hits []Hit) ([]byte, error) {
	var out []byte
	for _, h := range hits {
		buf := make([]byte, h.Entry.Size)
		if _, err := r.ReadAt(buf, h.Entry.Offset); err != nil {
			return nil, fault.New(fault.ErrCorruptBox, h.Entry.Offset,
				"sample %d of track %d unreadable: %v",
				h.Entry.SampleIndex, h.Entry.TrackID, err).WithTrack(h.Entry.TrackID)
		}
		out = append(out, buf...)
	}
	return out, nil
}
