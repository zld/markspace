// Package index rebuilds the random-access index from repaired fragment
// timestamps. Every entry points back at the source fragment and its raw
// byte offsets; queries return the latest decodable (sync) sample not
// later than the target time, and out-of-range queries fail with an
// explicit boundary error.
package index

import (
	"errors"
	"fmt"
	"sort"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/parse"
)

// Boundary errors for out-of-range queries.
var (
	ErrBeforeStart = errors.New("index: query before first random access point")
	ErrAfterEnd    = errors.New("index: query past end of indexed media")
)

// Entry is one random-access point.
type Entry struct {
	Tick        clock.Ticks `json:"tick"`        // decode time on the master timeline
	Fragment    string      `json:"fragment"`    // source fragment path
	MoofOffset  int64       `json:"moofOffset"`  // raw offset of the moof in Fragment
	SampleIndex int         `json:"sampleIndex"` // sample index within the fragment
	DataOffset  int64       `json:"dataOffset"`  // raw offset of the sample payload
	SourceDigest string     `json:"sourceDigest"` // sha256 of the source fragment file
}

// Index is an ordered set of random-access entries over one track.
type Index struct {
	Entries []Entry     `json:"entries"`
	End     clock.Ticks `json:"end"` // end of indexed media (exclusive)
}

// Source couples one parsed fragment with its repaired timestamps.
type Source struct {
	Fragment      *parse.Fragment
	Repaired      []uint64 // repaired decode times, track timescale units
	Timescale     int64
	TimelineStart clock.Ticks // where the fragment's media starts on the master timeline
	Digest        string      // sha256 of the fragment file
}

// Build collects the sync samples of all sources into one ordered index.
func Build(srcs []Source) (*Index, error) {
	ix := &Index{}
	for _, src := range srcs {
		f := src.Fragment
		if len(src.Repaired) != len(f.Samples) {
			return nil, fault.New(fault.StageIndex,
				fmt.Sprintf("repaired/sample count mismatch %d != %d",
					len(src.Repaired), len(f.Samples))).At(f.Path, f.MoofOffset)
		}
		var maxEnd clock.Ticks
		for i, s := range f.Samples {
			tick, err := clock.ToMaster(int64(src.Repaired[i]), src.Timescale)
			if err != nil {
				return nil, fault.New(fault.StageIndex, "bad timescale").
					At(f.Path, f.MoofOffset).Wrap(err)
			}
			tick += src.TimelineStart
			dur := clock.MustToMaster(int64(s.Duration), src.Timescale)
			if tick+dur > maxEnd {
				maxEnd = tick + dur
			}
			if !s.Sync {
				continue
			}
			ix.Entries = append(ix.Entries, Entry{
				Tick:         tick,
				Fragment:     f.Path,
				MoofOffset:   f.MoofOffset,
				SampleIndex:  s.Index,
				DataOffset:   s.DataOffset,
				SourceDigest: src.Digest,
			})
		}
		if maxEnd > ix.End {
			ix.End = maxEnd
		}
	}
	sort.Slice(ix.Entries, func(i, j int) bool {
		a, b := ix.Entries[i], ix.Entries[j]
		if a.Tick != b.Tick {
			return a.Tick < b.Tick
		}
		if a.Fragment != b.Fragment {
			return a.Fragment < b.Fragment
		}
		return a.SampleIndex < b.SampleIndex
	})
	if len(ix.Entries) == 0 {
		return nil, fault.New(fault.StageIndex, "no random access points found")
	}
	return ix, nil
}

// Query returns the entry with the greatest tick not later than t.
// Queries outside [first entry, End) fail with a boundary error.
func (ix *Index) Query(t clock.Ticks) (Entry, error) {
	if len(ix.Entries) == 0 {
		return Entry{}, ErrBeforeStart
	}
	if t < ix.Entries[0].Tick {
		return Entry{}, fmt.Errorf("%w: %d < %d", ErrBeforeStart, t, ix.Entries[0].Tick)
	}
	if t >= ix.End {
		return Entry{}, fmt.Errorf("%w: %d >= %d", ErrAfterEnd, t, ix.End)
	}
	// Binary search: last entry with Tick <= t.
	lo, hi := 0, len(ix.Entries)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if ix.Entries[mid].Tick <= t {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return ix.Entries[lo], nil
}
