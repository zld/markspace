// Package seek locates the decodable sync sample preceding a target
// time or sample number, in O(log n) over the built index.
package seek

import (
	"errors"
	"fmt"
	"sort"

	"mp4seek/internal/index"
	"mp4seek/internal/timeline"
)

// Status explains why a target could not be located.
type Status string

const (
	StatusOK        Status = "ok"
	StatusHole      Status = "hole"       // target inside an empty edit
	StatusBeyondEnd Status = "beyond-end" // target past the last edit
	StatusNoSync    Status = "no-sync"    // no sync sample at or before target
)

// Error is a locate failure with a machine-readable status.
type Error struct {
	Status Status
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Status, e.Detail) }

// IsNotLocatable reports whether err is a locate failure (exit code 4).
func IsNotLocatable(err error) bool {
	var se *Error
	return errors.As(err, &se)
}

// Result is a single located landing point.
type Result struct {
	TrackID   int
	Sample    int // 0-based sample number
	PTS       int64
	DTS       int64
	CTS       int64
	Offset    int64
	Size      int64
	Sync      bool
	MediaTime int64 // resolved media time of the target
}

// LocateTime maps a movie-timeline time (movie timescale units) through
// the edit list and returns the latest sync sample with PTS <= target.
// A track without an edit list maps movie time to media time directly.
func LocateTime(ti *index.TrackIndex, movieTime, movieTS int64) (*Result, error) {
	if len(ti.Track.Edits) == 0 {
		if movieTime < 0 {
			return nil, &Error{StatusNoSync, fmt.Sprintf("negative movie time %d", movieTime)}
		}
		media := movieTime * ti.Track.Timescale / movieTS
		r, err := locateMedia(ti, media)
		if err != nil {
			return nil, err
		}
		r.MediaTime = media
		return r, nil
	}
	media, st := timeline.MapMovieTime(ti.Track.Edits, movieTime, movieTS, ti.Track.Timescale)
	switch st {
	case timeline.MapHole:
		return nil, &Error{StatusHole, fmt.Sprintf("movie time %d falls in an empty edit", movieTime)}
	case timeline.MapBeyondEnd:
		return nil, &Error{StatusBeyondEnd, fmt.Sprintf("movie time %d is past the last edit segment", movieTime)}
	}
	r, err := locateMedia(ti, media)
	if err != nil {
		return nil, err
	}
	r.MediaTime = media
	return r, nil
}

// LocateMediaTime locates by a media-timeline target directly.
func LocateMediaTime(ti *index.TrackIndex, mediaTime int64) (*Result, error) {
	return locateMedia(ti, mediaTime)
}

func locateMedia(ti *index.TrackIndex, mediaTime int64) (*Result, error) {
	if mediaTime < 0 {
		return nil, &Error{StatusNoSync, fmt.Sprintf("negative media time %d", mediaTime)}
	}
	syncIdx := ti.SyncSamples()
	// Binary search: last sync sample with PTS <= mediaTime.
	pos := sort.Search(len(syncIdx), func(i int) bool {
		return ti.Samples[syncIdx[i]].PTS > mediaTime
	}) - 1
	if pos < 0 {
		return nil, &Error{StatusNoSync, fmt.Sprintf("no sync sample with PTS <= %d", mediaTime)}
	}
	s := ti.Samples[syncIdx[pos]]
	return &Result{
		TrackID: ti.Track.ID, Sample: s.Index, PTS: s.PTS, DTS: s.DTS,
		CTS: s.CTS, Offset: s.Offset, Size: s.Size, Sync: s.Sync,
	}, nil
}

// LocateSample returns the sync sample at or before sample number n.
func LocateSample(ti *index.TrackIndex, n int) (*Result, error) {
	if n < 0 || n >= len(ti.Samples) {
		return nil, &Error{StatusBeyondEnd, fmt.Sprintf("sample %d out of range [0,%d)", n, len(ti.Samples))}
	}
	syncIdx := ti.SyncSamples()
	pos := sort.Search(len(syncIdx), func(i int) bool {
		return syncIdx[i] > n
	}) - 1
	if pos < 0 {
		return nil, &Error{StatusNoSync, fmt.Sprintf("no sync sample at or before sample %d", n)}
	}
	s := ti.Samples[syncIdx[pos]]
	return &Result{
		TrackID: ti.Track.ID, Sample: s.Index, PTS: s.PTS, DTS: s.DTS,
		CTS: s.CTS, Offset: s.Offset, Size: s.Size, Sync: s.Sync,
	}, nil
}
