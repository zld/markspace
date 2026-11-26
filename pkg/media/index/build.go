// Package index builds random-access indexes over repaired representations.
//
// An entry is anchored at every sync (random-access) sample. Lookup returns
// the entry with the greatest time not exceeding the target, i.e. the nearest
// decodable point from which a player can reach the requested instant. The
// index is sorted and searched in memory but built from streaming scans, so
// its memory footprint is one entry per sync sample rather than per sample.
package index

import (
	"sort"

	"cmafrepair/pkg/media/cmaf"
	"cmafrepair/pkg/media/timebase"
)

// Entry is one random-access point.
type Entry struct {
	Representation string `json:"representation"`
	Fragment       string `json:"fragment"`
	Sequence       uint32 `json:"sequence"`
	SampleIndex    uint32 `json:"sample_index"`
	// MediaTime is the sample's base media decode time in track timescale.
	MediaTime uint64 `json:"media_time"`
	// SharedMs is the same instant on the shared millisecond clock.
	SharedMs int64 `json:"shared_ms"`
	// FileOffset is the absolute byte offset of the fragment's mdat payload.
	FileOffset int64 `json:"file_offset"`
	// SampleOffset is the byte offset of the sample inside the mdat payload.
	SampleOffset int64 `json:"sample_offset"`
	Gap          bool  `json:"gap"`
}

// Index is the sorted random-access table for one representation.
type Index struct {
	Representation string  `json:"representation"`
	Timescale      uint32  `json:"timescale"`
	Entries        []Entry `json:"entries"`
}

// Len/Less/Swap keep Entries binary-searchable.
func (ix *Index) Len() int      { return len(ix.Entries) }
func (ix *Index) Swap(a, b int) { ix.Entries[a], ix.Entries[b] = ix.Entries[b], ix.Entries[a] }
func (ix *Index) Less(a, b int) bool {
	x, y := ix.Entries[a], ix.Entries[b]
	if x.SharedMs != y.SharedMs {
		return x.SharedMs < y.SharedMs
	}
	// Decodable points order before gap placeholders at the same instant.
	if x.Gap != y.Gap {
		return !x.Gap
	}
	if x.Sequence != y.Sequence {
		return x.Sequence < y.Sequence
	}
	return x.SampleIndex < y.SampleIndex
}

// Builder accumulates sync-sample entries for one representation.
type Builder struct {
	rep       string
	timescale uint32
	entries   []Entry
}

// NewBuilder starts an index for a representation.
func NewBuilder(rep string, timescale uint32) *Builder {
	return &Builder{rep: rep, timescale: timescale}
}

// AddFragment records one entry per sync sample of a repaired fragment.
// Sample media times are derived from the repaired tfdt (e.FixedTfdt) plus
// cumulative sample durations, so the builder does not depend on the repair
// pass having mutated the fragment. Sample payload offsets accumulate from
// trun sizes relative to the mdat payload start.
func (b *Builder) AddFragment(f *cmaf.Fragment, e cmaf.TimelineEntry) error {
	if b.timescale == 0 {
		return timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseIndex,
			"index builder for %s has no timescale", b.rep)
	}
	mdatPayload := f.Mdat.PayloadOff
	var sampleCursor int64
	mediaTime := e.FixedTfdt
	for i := range f.Samples {
		s := &f.Samples[i]
		if s.Sync {
			// Sample media time in shared ms.
			ms, err := timebase.ToSharedMs(mediaTime, b.timescale)
			if err != nil {
				return err
			}
			b.entries = append(b.entries, Entry{
				Representation: b.rep,
				Fragment:       f.Name,
				Sequence:       f.Sequence,
				SampleIndex:    s.Index,
				MediaTime:      mediaTime,
				SharedMs:       ms,
				FileOffset:     mdatPayload,
				SampleOffset:   sampleCursor,
				Gap:            e.Status == cmaf.StatusGap || e.Status == cmaf.StatusTruncated,
			})
		}
		sampleCursor += int64(s.Size)
		mediaTime += uint64(s.Duration)
	}
	return nil
}

// AddGap records a GAP placeholder so the manifest/index can name a missing
// range without pointing at decodable bytes.
func (b *Builder) AddGap(e cmaf.TimelineEntry) {
	b.entries = append(b.entries, Entry{
		Representation: b.rep,
		Fragment:       e.Name,
		Sequence:       e.Sequence,
		SharedMs:       e.StartMs,
		Gap:            true,
		FileOffset:     -1,
		SampleOffset:   -1,
	})
}

// Build finalizes and returns a sorted index.
func (b *Builder) Build() *Index {
	ix := &Index{
		Representation: b.rep,
		Timescale:      b.timescale,
		Entries:        append([]Entry{}, b.entries...),
	}
	sort.Sort(ix)
	return ix
}

// Lookup returns the nearest decodable (non-gap) entry at or before targetMs.
// It returns false when no decodable point exists that early.
func (ix *Index) Lookup(targetMs int64) (Entry, bool) {
	// Find the first entry strictly after target, then step back.
	n := len(ix.Entries)
	pos := sort.Search(n, func(i int) bool {
		return ix.Entries[i].SharedMs > targetMs
	})
	for i := pos - 1; i >= 0; i-- {
		if !ix.Entries[i].Gap {
			return ix.Entries[i], true
		}
	}
	return Entry{}, false
}

// LookupOrFirst returns the nearest point before target, or when the target
// precedes every entry, the first decodable entry (the earliest seek point).
func (ix *Index) LookupOrFirst(targetMs int64) (Entry, bool) {
	if e, ok := ix.Lookup(targetMs); ok {
		return e, true
	}
	for _, e := range ix.Entries {
		if !e.Gap {
			return e, true
		}
	}
	return Entry{}, false
}

// DecodableCount returns the number of non-gap entries.
func (ix *Index) DecodableCount() int {
	n := 0
	for _, e := range ix.Entries {
		if !e.Gap {
			n++
		}
	}
	return n
}

// GapCount returns the number of gap placeholders.
func (ix *Index) GapCount() int {
	n := 0
	for _, e := range ix.Entries {
		if e.Gap {
			n++
		}
	}
	return n
}
