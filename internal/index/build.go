// Package index builds and queries a per-sample random-access index over
// repaired tracks.
package index

import (
	"sort"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

// Entry is one indexed sample.
type Entry struct {
	TrackID  uint32
	Number   int
	DTS      media.Rational
	End      media.Rational
	PTS      media.Rational
	Offset   int64
	Size     uint32
	Keyframe bool
}

// TrackIndex is the ordered sample index for one track.
type TrackIndex struct {
	TrackID uint32
	Kind    parse.Kind
	Entries []Entry
}

// Index is the full program index.
type Index struct {
	Tracks []*TrackIndex
}

// Build indexes every sample of every track. Track order is preserved and
// sample order follows the repaired decode timeline.
func Build(asset *parse.Asset, repaired map[uint32][]parse.Sample) (*Index, error) {
	if asset == nil {
		return nil, media.New(media.CodeConfig, "index.build", -1, "nil asset")
	}
	idx := &Index{}
	for _, t := range asset.Tracks {
		samples, ok := repaired[t.ID]
		if !ok {
			samples = t.Samples
		}
		ti := &TrackIndex{TrackID: t.ID, Kind: t.Kind, Entries: make([]Entry, 0, len(samples))}
		for _, s := range samples {
			ti.Entries = append(ti.Entries, Entry{
				TrackID: t.ID, Number: s.Number, DTS: s.DTS, End: s.End(),
				PTS: s.PTS, Offset: s.Offset, Size: s.Size, Keyframe: s.Keyframe,
			})
		}
		idx.Tracks = append(idx.Tracks, ti)
	}
	return idx, nil
}

// Track returns the index for a track id, or nil.
func (idx *Index) Track(id uint32) *TrackIndex {
	for _, t := range idx.Tracks {
		if t.TrackID == id {
			return t
		}
	}
	return nil
}

// SeekResult is a decodable random-access entry.
type SeekResult struct {
	Entry Entry
	// Deviation is the distance from the requested time back to the entry.
	Deviation media.Rational
	// Exact reports that the entry starts exactly at the target.
	Exact bool
}

// Seek returns the latest keyframe entry whose decode time is at or before
// target. For audio every packet qualifies; for video only entries marked
// keyframe do. When target precedes the first keyframe the first keyframe is
// returned, flagged non-exact, because it is the earliest decodable point.
func (t *TrackIndex) Seek(target media.Rational) (SeekResult, error) {
	if len(t.Entries) == 0 {
		return SeekResult{}, &media.Error{
			Code: media.CodeNoKeyframe, Op: "index.seek", TrackID: t.TrackID, Offset: -1,
			Msg: "track has no indexed samples",
		}
	}
	best := -1
	for i := range t.Entries {
		e := t.Entries[i]
		if !e.Keyframe {
			continue
		}
		if e.DTS.LessEq(target) {
			best = i
			continue
		}
		break
	}
	if best < 0 {
		for i := range t.Entries {
			if t.Entries[i].Keyframe {
				best = i
				break
			}
		}
	}
	if best < 0 {
		return SeekResult{}, &media.Error{
			Code: media.CodeNoKeyframe, Op: "index.seek", TrackID: t.TrackID, Offset: -1,
			Msg: "no keyframe entries",
		}
	}
	e := t.Entries[best]
	return SeekResult{
		Entry:     e,
		Deviation: target.Sub(e.DTS).Abs(),
		Exact:     target.Cmp(e.DTS) == 0,
	}, nil
}

// SeekVideo is a convenience wrapper that validates the track kind.
func (idx *Index) SeekVideo(trackID uint32, target media.Rational) (SeekResult, error) {
	t := idx.Track(trackID)
	if t == nil {
		return SeekResult{}, &media.Error{Code: media.CodeNoTrack, Op: "index.seek", TrackID: trackID, Offset: -1, Msg: "unknown track"}
	}
	if t.Kind != parse.KindVideo {
		return SeekResult{}, &media.Error{Code: media.CodeConfig, Op: "index.seek", TrackID: trackID, Offset: -1, Msg: "not a video track"}
	}
	return t.Seek(target)
}

// Range returns the entries of trackID whose DTS lies in [from, to).
func (idx *Index) Range(trackID uint32, from, to media.Rational) []Entry {
	t := idx.Track(trackID)
	if t == nil {
		return nil
	}
	start := sort.Search(len(t.Entries), func(i int) bool { return !t.Entries[i].DTS.Less(from) })
	var out []Entry
	for i := start; i < len(t.Entries); i++ {
		if !t.Entries[i].DTS.Less(to) {
			break
		}
		out = append(out, t.Entries[i])
	}
	return out
}

// ByteRanges returns the offsets and sizes of the given entries in track
// order, preserving index order.
func ByteRanges(entries []Entry) []ByteRange {
	out := make([]ByteRange, 0, len(entries))
	for _, e := range entries {
		out = append(out, ByteRange{Offset: e.Offset, Size: int64(e.Size)})
	}
	return out
}

// ByteRange is a half-open byte interval in the source file.
type ByteRange struct {
	Offset int64
	Size   int64
}

// MergeRanges coalesces overlapping or adjacent byte ranges into a minimal
// sorted set. It is used to report the exact byte coverage of a clip.
func MergeRanges(ranges []ByteRange) []ByteRange {
	if len(ranges) == 0 {
		return nil
	}
	cp := append([]ByteRange(nil), ranges...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].Offset != cp[j].Offset {
			return cp[i].Offset < cp[j].Offset
		}
		return cp[i].Size < cp[j].Size
	})
	out := []ByteRange{cp[0]}
	for _, r := range cp[1:] {
		last := &out[len(out)-1]
		if r.Offset <= last.Offset+last.Size {
			end := last.Offset + last.Size
			if r.Offset+r.Size > end {
				end = r.Offset + r.Size
			}
			last.Size = end - last.Offset
			continue
		}
		out = append(out, r)
	}
	return out
}

// TotalBytes sums range sizes.
func TotalBytes(ranges []ByteRange) int64 {
	var n int64
	for _, r := range ranges {
		n += r.Size
	}
	return n
}
