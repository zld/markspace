// Package index rebuilds a random-access index from repaired samples and can
// write it back into a playable MP4. Queries resolve a target time to the
// nearest keyframe at or before it.
package index

import (
	"encoding/json"
	"errors"
	"os"
	"sort"

	"archivist/internal/fault"
	"archivist/internal/repair"
)

// Version identifies the index layout recorded in provenance data.
const Version = "index/v1"

// ErrUnknownTrack is returned when a query names a track that is not indexed.
var ErrUnknownTrack = errors.New("index: unknown track")

// Entry is one sample's position on the repaired timeline.
type Entry struct {
	Sample    int    `json:"sample"`
	PTS       int64  `json:"pts"`        // repaired, track timescale
	MovieTime int64  `json:"movie_time"` // repaired, movie timescale
	Offset    int64  `json:"offset"`     // byte offset in the rebuilt file
	Size      uint32 `json:"size"`
	Keyframe  bool   `json:"keyframe"`
	Original  int64  `json:"original"` // stored value before repair
}

// Track is the index for one track.
type Track struct {
	ID             uint32 `json:"id"`
	Timescale      uint32 `json:"timescale"`
	MovieTimescale uint32 `json:"movie_timescale"`
	// OutputBase is subtracted from PTS to obtain the timestamp written into
	// the rebuilt file. PTS values in this index are always absolute.
	OutputBase int64   `json:"output_base"`
	Entries    []Entry `json:"entries"`
	keyframes  []int   // indices into Entries, ascending by PTS
}

// Index is the rebuilt index for one output file.
type Index struct {
	Version string   `json:"version"`
	Tracks  []*Track `json:"tracks"`
}

// EntryCount totals entries across tracks.
func (ix *Index) EntryCount() int {
	n := 0
	for _, t := range ix.Tracks {
		n += len(t.Entries)
	}
	return n
}

// KeyframeCount totals keyframe entries across tracks.
func (ix *Index) KeyframeCount() int {
	n := 0
	for _, t := range ix.Tracks {
		n += len(t.keyframes)
	}
	return n
}

// Track returns the index for trackID.
func (ix *Index) Track(trackID uint32) (*Track, bool) {
	for _, t := range ix.Tracks {
		if t.ID == trackID {
			return t, true
		}
	}
	return nil, false
}

// Hit is the answer to a random-access query.
type Hit struct {
	TrackID   uint32 `json:"track_id"`
	Sample    int    `json:"sample"`
	PTS       int64  `json:"pts"`
	MovieTime int64  `json:"movie_time"`
	Offset    int64  `json:"offset"`
	Size      uint32 `json:"size"`
	Delta     int64  `json:"delta"` // target - keyframe PTS, in track ticks
	Available bool   `json:"available"`
}

// Build converts a repaired result into an index with source-relative
// offsets. Offsets are replaced by OutputOffsets once the file is written.
func Build(rep *repair.Result) (*Index, error) {
	if rep == nil {
		return nil, fault.New(fault.KindInvariant, fault.StageIndex, "nil repair result")
	}
	ix := &Index{Version: Version}
	for _, tr := range rep.Tracks {
		if len(tr.Entries) == 0 {
			return nil, fault.New(fault.KindInvariant, fault.StageIndex,
				"track %d has no repaired entries", tr.TrackID)
		}
		t := &Track{
			ID:             tr.TrackID,
			Timescale:      tr.Timescale,
			MovieTimescale: tr.MovieTimescale,
		}
		var prev int64
		for i, e := range tr.Entries {
			if i > 0 && e.Repaired <= prev {
				return nil, fault.New(fault.KindInvariant, fault.StageIndex,
					"track %d sample %d is not monotonic", tr.TrackID, e.Sample)
			}
			prev = e.Repaired
			t.Entries = append(t.Entries, Entry{
				Sample:    e.Sample,
				PTS:       e.Repaired,
				MovieTime: e.MovieTime,
				Offset:    e.Offset,
				Size:      e.Size,
				Keyframe:  e.Keyframe,
				Original:  e.Original,
			})
		}
		t.buildKeyframes()
		ix.Tracks = append(ix.Tracks, t)
	}
	return ix, nil
}

func (t *Track) buildKeyframes() {
	t.keyframes = t.keyframes[:0]
	for i, e := range t.Entries {
		if e.Keyframe {
			t.keyframes = append(t.keyframes, i)
		}
	}
	if len(t.keyframes) == 0 && len(t.Entries) > 0 {
		// A track with no declared sync samples is decodable only from its
		// first sample, so treat that sample as the sole entry point.
		t.keyframes = append(t.keyframes, 0)
	}
}

// Query returns the nearest keyframe with PTS at or before targetTicks, where
// targetTicks is expressed in the track timescale. Delta is the distance from
// that keyframe to the target and is never negative. When the target precedes
// every keyframe, Hit.Available is false and the first keyframe is returned
// with its negative distance so callers can clamp to the start.
func (ix *Index) Query(trackID uint32, targetTicks int64) (Hit, error) {
	t, ok := ix.Track(trackID)
	if !ok {
		return Hit{}, fault.New(fault.KindInvariant, fault.StageIndex,
			"query for unknown track %d", trackID).Wrap(ErrUnknownTrack)
	}
	if len(t.Entries) == 0 || len(t.keyframes) == 0 {
		return Hit{}, fault.New(fault.KindInvariant, fault.StageIndex,
			"track %d has no index entries", trackID)
	}
	// Binary search for the last keyframe whose PTS <= target.
	lo, hi := 0, len(t.keyframes)
	for lo < hi {
		mid := (lo + hi) / 2
		if t.Entries[t.keyframes[mid]].PTS <= targetTicks {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		e := t.Entries[t.keyframes[0]]
		return Hit{
			TrackID: trackID, Sample: e.Sample, PTS: e.PTS, MovieTime: e.MovieTime,
			Offset: e.Offset, Size: e.Size, Delta: targetTicks - e.PTS, Available: false,
		}, nil
	}
	e := t.Entries[t.keyframes[lo-1]]
	return Hit{
		TrackID: trackID, Sample: e.Sample, PTS: e.PTS, MovieTime: e.MovieTime,
		Offset: e.Offset, Size: e.Size, Delta: targetTicks - e.PTS, Available: true,
	}, nil
}

// QueryMovie resolves a target expressed in the movie timescale.
func (ix *Index) QueryMovie(trackID uint32, movieTicks int64) (Hit, error) {
	t, ok := ix.Track(trackID)
	if !ok {
		return Hit{}, fault.New(fault.KindInvariant, fault.StageIndex,
			"query for unknown track %d", trackID).Wrap(ErrUnknownTrack)
	}
	if t.MovieTimescale == 0 || t.Timescale == 0 || t.MovieTimescale == t.Timescale {
		return ix.Query(trackID, movieTicks)
	}
	return ix.Query(trackID, scale(movieTicks, t.MovieTimescale, t.Timescale))
}

func scale(v int64, from, to uint32) int64 {
	if from == 0 || to == 0 || from == to {
		return v
	}
	neg := v < 0
	if neg {
		v = -v
	}
	out := (v*int64(to) + int64(from)/2) / int64(from)
	if neg {
		out = -out
	}
	return out
}

// SampleOffsets replaces entry offsets with the absolute byte offsets inside
// the rebuilt file. tracks maps track ID to its packed sample offsets.
func (ix *Index) SampleOffsets(offsets map[uint32][]int64) error {
	for _, t := range ix.Tracks {
		off, ok := offsets[t.ID]
		if !ok {
			return fault.New(fault.KindInvariant, fault.StageIndex,
				"missing output offsets for track %d", t.ID)
		}
		if len(off) != len(t.Entries) {
			return fault.New(fault.KindInvariant, fault.StageIndex,
				"track %d offset count %d != entry count %d", t.ID, len(off), len(t.Entries))
		}
		for i := range t.Entries {
			t.Entries[i].Offset = off[i]
		}
	}
	return nil
}

// SetRebase records, per track, the constant subtracted from absolute PTS in
// the rebuilt file so that the on-disk timestamps fit in 32 bits.
func (ix *Index) SetRebase(bases map[uint32]int64) error {
	for _, t := range ix.Tracks {
		b, ok := bases[t.ID]
		if !ok {
			return fault.New(fault.KindInvariant, fault.StageIndex,
				"missing rebase value for track %d", t.ID)
		}
		t.OutputBase = b
	}
	return nil
}

// SortedTrackIDs returns track IDs in ascending order.
func (ix *Index) SortedTrackIDs() []uint32 {
	ids := make([]uint32, 0, len(ix.Tracks))
	for _, t := range ix.Tracks {
		ids = append(ids, t.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Load reads a previously written index artifact.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New(fault.KindIO, fault.StageIndex, "read index").In(path).Wrap(err)
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fault.New(fault.KindInvariant, fault.StageIndex, "parse index").In(path).Wrap(err)
	}
	for _, t := range ix.Tracks {
		t.buildKeyframes()
	}
	return &ix, nil
}
