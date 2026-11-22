// Package index builds keyframe lookup tables from parsed sample tables and
// repairs incomplete indexes by scanning readable media data.
package index

import (
	"errors"
	"sort"

	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/mp4"
)

// Entry is a keyframe location on the presentation timeline.
type Entry struct {
	TrackID     uint32
	Sample      int
	DecodeTicks uint64
	Offset      int64 // absolute byte offset in the file
	Presented   int64 // unified presentation ticks (ns)
	Exact       bool  // false when the landing point was inferred
}

// TrackIndex is the keyframe view of one track.
type TrackIndex struct {
	TrackID    uint32
	Entries    []Entry
	ExactCount int
	Inferred   int
	Rebuilt    bool
	// Duration of the media in presentation ticks.
	Duration int64
	Warnings []*fault.Error
}

// Index is the keyframe index of a movie.
type Index struct {
	Path     string
	Tracks   []*TrackIndex
	Warnings []*fault.Error
	Rebuilt  bool
}

// KeyframeAtOrBefore returns the newest entry presented at or before t.
func (ti *TrackIndex) KeyframeAtOrBefore(t int64) (Entry, bool) {
	i := sort.Search(len(ti.Entries), func(i int) bool { return ti.Entries[i].Presented > t })
	if i == 0 {
		return Entry{}, false
	}
	return ti.Entries[i-1], true
}

// AtOrAfter returns the first entry with presented time >= t.
// ExactRate reports the share of entries aligned on a real sync sample.
func (ti *TrackIndex) ExactRate() float64 {
	if len(ti.Entries) == 0 {
		return 0
	}
	return float64(ti.ExactCount) / float64(len(ti.Entries))
}

// Presentation resolves a sample's unified presentation time. Implementations
// are expected to compute each timestamp once per track.
type Presentation interface {
	At(trackID uint32, sample int) (int64, bool)
}

// Build constructs keyframe indexes for every video track. sampleClocks must
// provide a presentation mapping per track ID. Audio tracks are skipped since
// every audio frame is independently decodable.
func Build(movie *mp4.Movie, pres Presentation) (*Index, error) {
	ix := &Index{Path: movie.Path}
	var problems []error
	for _, tr := range movie.Tracks {
		if tr.IsAudio() || len(tr.Samples) == 0 {
			continue
		}
		ti := &TrackIndex{TrackID: tr.ID}
		var lastPresented int64
		for i, s := range tr.Samples {
			presented, ok := pres.At(tr.ID, i)
			if !ok {
				e := fault.Errorf("index.build", fault.KindIndex,
					"sample %d has no usable presentation time", i).
					WithPath(movie.Path).WithTrack(tr.ID).WithSample(i).Recovered()
				ti.Warnings = append(ti.Warnings, e)
				continue
			}
			lastPresented = presented
			sync := s.Sync
			exact := true
			if !tr.SyncTablePresent {
				// Absent stss implies every sample is a sync sample, but the
				// landing point becomes inferred rather than declared.
				sync, exact = true, false
			}
			if !sync {
				continue
			}
			ti.Entries = append(ti.Entries, Entry{
				TrackID: tr.ID, Sample: i, DecodeTicks: s.DTS,
				Offset: s.Offset, Presented: presented, Exact: exact,
			})
			if exact {
				ti.ExactCount++
			} else {
				ti.Inferred++
			}
		}
		ti.Duration = lastPresented
		ix.Tracks = append(ix.Tracks, ti)
		ix.Warnings = append(ix.Warnings, ti.Warnings...)
		if len(ti.Entries) == 0 {
			e := fault.Errorf("index.build", fault.KindIndex, "track %d has no sync frames", tr.ID).
				WithPath(movie.Path).WithTrack(tr.ID).Recovered().Wrapped(fault.ErrNoKeyframe)
			ix.Warnings, problems = append(ix.Warnings, e), append(problems, e)
		}
	}
	if len(ix.Tracks) == 0 {
		e := fault.Errorf("index.build", fault.KindIndex, "no video tracks to index").
			WithPath(movie.Path).Fatal().Wrapped(fault.ErrIncomplete)
		return ix, e
	}
	ix.Rebuilt = movieRebuilt(movie)
	if len(problems) > 0 {
		return ix, fault.New("index.build", fault.KindIndex, joinErrors(problems)).WithPath(movie.Path).Recovered()
	}
	return ix, nil
}

func movieRebuilt(m *mp4.Movie) bool {
	for _, tr := range m.Tracks {
		if !tr.IndexComplete {
			return true
		}
	}
	return false
}

func joinErrors(errs []error) error { return errors.Join(errs...) }

// Align picks the sync entry at or before t. The returned gap is the distance
// back from t; a zero gap means the cut landed exactly on a keyframe.
func (ti *TrackIndex) Align(t int64) (Entry, int64, error) {
	e, ok := ti.KeyframeAtOrBefore(t)
	if !ok {
		return Entry{}, 0, fault.Errorf("index.align", fault.KindIndex,
			"no sync frame at or before %d on track %d", t, ti.TrackID).
			WithTrack(ti.TrackID).Wrapped(fault.ErrNoKeyframe)
	}
	return e, t - e.Presented, nil
}

// RebuildByScan reconstructs an index from readable samples when stss is
// missing or damaged. Samples are assumed sync at chunk starts, which the
// sample tables encode through stsc; the result is marked inferred.
func RebuildByScan(movie *mp4.Movie, tr *mp4.Track, pres Presentation) *TrackIndex {
	ti := &TrackIndex{TrackID: tr.ID, Rebuilt: true}
	if len(tr.Samples) == 0 {
		return ti
	}
	starts := chunkStarts(tr)
	for i, s := range tr.Samples {
		if _, ok := starts[s.Offset]; !ok && i != 0 {
			continue
		}
		presented, ok := pres.At(tr.ID, i)
		if !ok {
			continue
		}
		ti.Entries = append(ti.Entries, Entry{
			TrackID: tr.ID, Sample: i, DecodeTicks: s.DTS, Offset: s.Offset,
			Presented: presented, Exact: false,
		})
		ti.Inferred++
	}
	if n := len(ti.Entries); n > 0 {
		ti.Duration = ti.Entries[n-1].Presented
	}
	return ti
}

func chunkStarts(tr *mp4.Track) map[int64]struct{} {
	out := make(map[int64]struct{}, len(tr.ChunkOffsets))
	for _, off := range tr.ChunkOffsets {
		out[off] = struct{}{}
	}
	return out
}
