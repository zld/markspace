// Package timeline maps raw media timelines onto one exact presentation
// timeline. Edit lists are applied first (trim, empty edits, rate), then every
// track's presented start is normalised against the version origin so multiple
// renditions of the same asset share a common time base. No step uses floats.
package timeline

import (
	"errors"
	"fmt"
	"sort"

	"mediapipe/internal/model"
)

// Errors produced during mapping.
var (
	ErrNotPresented = errors.New("timeline: timestamp not presented by edit list")
	ErrBadRate      = errors.New("timeline: non-positive edit rate")
)

// Edit is one elst entry. MediaTime < 0 denotes an empty edit that advances
// the presentation timeline without consuming media. DurationTicks is in the
// track timescale; Rate is media ticks per presentation tick.
type Edit struct {
	MediaTime     int64
	DurationTicks int64
	Rate          model.Rational
}

// EditList is an ordered elst.
type EditList []Edit

// IdentityEdits is the implicit edit list when a track has none: the whole
// media is presented from tick 0 at rate 1.
func IdentityEdits() EditList {
	return EditList{{MediaTime: 0, DurationTicks: 0, Rate: model.Rational{Num: 1, Den: 1}}}
}

// Segment is a raw published interval in media ticks.
type Segment struct {
	StartTicks    int64
	DurationTicks int64
	Summary       string
	// Cells are content digests of fixed-size grid cells starting at the
	// segment's first cell, so versions that slice the same content
	// differently compare equal cell by cell.
	Cells    []string
	CellSize model.Nanos
	Samples  int
	Bytes    int64
	Sparse   bool
	GopTicks int64
}

// EndTicks returns the exclusive end in media ticks.
func (s Segment) EndTicks() int64 { return s.StartTicks + s.DurationTicks }

// Track is one track's mapping input.
type Track struct {
	Asset     string
	Version   string
	TrackID   string
	Kind      string
	Timescale uint32
	Edits     EditList
	Segments  []Segment
}

// MappedSegment is a segment after edit-list application and origin
// normalisation.
type MappedSegment struct {
	// Timeline is the presented half-open span relative to the version
	// origin. MediaTick retains the original raw start timestamp.
	Timeline  model.Span
	MediaTick int64
	Summary   string
	Cells     []string
	// CellSize is the duration of one cell, derived from the track timescale
	// and the publication grid.
	CellSize model.Nanos
	Samples  int
	Bytes    int64
	Sparse   bool
	GopTicks int64
}

// CellSlice returns the cells covering n..m within the segment.
func (s MappedSegment) CellSlice(start, end model.Nanos) ([]string, bool) {
	if s.CellSize <= 0 || len(s.Cells) == 0 {
		return nil, false
	}
	if start < s.Timeline.Start || end > s.Timeline.End || end < start {
		return nil, false
	}
	first := int64(start-s.Timeline.Start) / int64(s.CellSize)
	last := int64(end-s.Timeline.Start) / int64(s.CellSize)
	if first < 0 || last > int64(len(s.Cells)) || first > last {
		return nil, false
	}
	return s.Cells[first:last], true
}

// MappedTrack is a track on the common timeline.
type MappedTrack struct {
	Asset     string
	Version   string
	TrackID   string
	Kind      string
	Timescale uint32
	Edits     EditList
	// PresentedOrigin is the absolute presentation time of the track's
	// earliest presented segment, relative to the whole edit list start.
	PresentedOrigin model.Nanos
	// OriginShift is subtracted from absolute presentation times to obtain
	// the version-relative timeline; it equals the minimum PresentedOrigin
	// across the version's tracks.
	OriginShift model.Nanos
	Segments    []MappedSegment
}

// Start returns the first segment start on the normalised timeline.
func (t MappedTrack) Start() model.Nanos {
	if len(t.Segments) == 0 {
		return 0
	}
	return t.Segments[0].Timeline.Start
}

// End returns the last segment end on the normalised timeline.
func (t MappedTrack) End() model.Nanos {
	if len(t.Segments) == 0 {
		return 0
	}
	return t.Segments[len(t.Segments)-1].Timeline.End
}

// Span returns the whole normalised extent of the track.
func (t MappedTrack) Span() model.Span { return model.Span{Start: t.Start(), End: t.End()} }

// MapVersion maps every track of one version and normalises all tracks to the
// version origin: the earliest presented media time across its tracks.
func MapVersion(asset, version string, tracks []Track) ([]MappedTrack, error) {
	if len(tracks) == 0 {
		return nil, model.New(model.KindInvalid, model.StageTimeline, asset, "map",
			errors.New("version has no tracks")).WithVersion(version)
	}
	mapped := make([]MappedTrack, 0, len(tracks))
	origin := model.Nanos(0)
	for i, t := range tracks {
		mt, err := mapTrack(asset, version, t)
		if err != nil {
			return nil, err
		}
		mapped = append(mapped, mt)
		if i == 0 || mt.PresentedOrigin < origin {
			origin = mt.PresentedOrigin
		}
	}
	// The version origin is the earliest presented media time across tracks;
	// subtracting it places that track at zero and leaves every other track
	// at its true skew from the common base.
	for i := range mapped {
		mapped[i].OriginShift = origin
		for j := range mapped[i].Segments {
			mapped[i].Segments[j].Timeline.Start -= origin
			mapped[i].Segments[j].Timeline.End -= origin
		}
	}
	return mapped, nil
}

func mapErr(asset, version, track, op string, cause error, off int64, hasOff bool) error {
	e := model.New(model.KindInvalid, model.StageTimeline, asset, op, cause).
		WithVersion(version).WithTrack(track)
	if hasOff {
		e = e.WithOffset(off)
	}
	return e
}

func mapTrack(asset, version string, t Track) (MappedTrack, error) {
	if t.Timescale == 0 {
		return MappedTrack{}, mapErr(asset, version, t.TrackID, "map", model.ErrZeroTimescale, 0, false)
	}
	edits := t.Edits
	if len(edits) == 0 {
		edits = IdentityEdits()
	}
	segments := append([]Segment(nil), t.Segments...)
	sort.Slice(segments, func(i, j int) bool { return segments[i].StartTicks < segments[j].StartTicks })

	out := MappedTrack{
		Asset: asset, Version: version, TrackID: t.TrackID, Kind: t.Kind,
		Timescale: t.Timescale, Edits: edits,
	}
	for _, s := range segments {
		spans, err := mapSegment(edits, s, t.Timescale)
		if err != nil {
			if errors.Is(err, ErrNotPresented) {
				// Fully trimmed content is not part of the presentation.
				continue
			}
			return MappedTrack{}, mapErr(asset, version, t.TrackID, "map", err, s.StartTicks, true)
		}
		for _, piece := range spans {
			cells := cellsFor(piece.offset, piece.span.Duration(), s)
			out.Segments = append(out.Segments, MappedSegment{
				Timeline: piece.span, MediaTick: s.StartTicks + piece.offsetTicks, Summary: s.Summary,
				Cells: cells, CellSize: s.CellSize,
				Samples: s.Samples, Bytes: s.Bytes, Sparse: s.Sparse, GopTicks: s.GopTicks,
			})
		}
	}
	if len(out.Segments) == 0 {
		return MappedTrack{}, mapErr(asset, version, t.TrackID, "map",
			errors.New("edit list trims every segment"), 0, false)
	}
	sort.SliceStable(out.Segments, func(i, j int) bool {
		return out.Segments[i].Timeline.Start < out.Segments[j].Timeline.Start
	})
	out.PresentedOrigin = out.Segments[0].Timeline.Start
	return out, nil
}

// spanPiece is one presented piece of a source segment. Offset is the piece's
// start relative to the segment, so per-cell digests can be sliced correctly
// even when an edit list splits one published range into several pieces.
type spanPiece struct {
	span        model.Span
	offset      model.Nanos
	offsetTicks int64
}

// cellsFor returns the subset of the segment's cells covering a piece that
// starts at offset, or nil when cells are unavailable or misaligned.
func cellsFor(offset, duration model.Nanos, s Segment) []string {
	if len(s.Cells) == 0 || s.CellSize <= 0 {
		return nil
	}
	first := int64(offset) / int64(s.CellSize)
	last := int64(offset+duration) / int64(s.CellSize)
	if first < 0 || last > int64(len(s.Cells)) || first >= last {
		return nil
	}
	return append([]string(nil), s.Cells[first:last]...)
}

// mapSegment returns the presented sub-spans of s. A segment may be clipped by
// an edit boundary or split into pieces by a later media edit.
func mapSegment(edits EditList, s Segment, timescale uint32) ([]spanPiece, error) {
	if s.DurationTicks <= 0 {
		return nil, fmt.Errorf("timeline: segment duration %d", s.DurationTicks)
	}
	var out []spanPiece
	var presented model.Nanos
	for _, e := range edits {
		if e.Rate.Den == 0 || e.Rate.Num <= 0 || e.Rate.Den < 0 {
			return nil, ErrBadRate
		}
		if e.DurationTicks < 0 {
			return nil, fmt.Errorf("timeline: negative edit duration %d", e.DurationTicks)
		}
		editSpan, err := editPRSpan(e, timescale)
		if err != nil {
			return nil, err
		}
		if e.MediaTime < 0 {
			// Empty edit: shifts the presentation and consumes no media.
			presented += editSpan
			continue
		}
		mediaLen, err := editMediaLen(e)
		if err != nil {
			return nil, err
		}
		lo := s.StartTicks
		if e.MediaTime > lo {
			lo = e.MediaTime
		}
		hi := s.EndTicks()
		if mediaLen < 1<<62 && e.MediaTime+mediaLen < hi {
			hi = e.MediaTime + mediaLen
		}
		if hi > lo {
			a, err := mediaToPR(e, lo, timescale)
			if err != nil {
				return nil, err
			}
			b, err := mediaToPR(e, hi, timescale)
			if err != nil {
				return nil, err
			}
			if b > a {
				off, err := model.NanosFromRatio(lo-s.StartTicks, int64(timescale))
				if err != nil {
					return nil, err
				}
				out = append(out, spanPiece{
					span:        model.Span{Start: presented + a, End: presented + b},
					offset:      off,
					offsetTicks: lo - s.StartTicks,
				})
			}
		}
		presented += editSpan
	}
	if len(out) == 0 {
		return nil, ErrNotPresented
	}
	return out, nil
}

// editMediaLen is the media length consumed by an edit, in media ticks. Zero
// duration means the edit runs to the end of the media.
func editMediaLen(e Edit) (int64, error) {
	v, err := model.MulDiv(e.DurationTicks, e.Rate.Num, e.Rate.Den)
	if err != nil {
		return 0, err
	}
	if v == 0 {
		return 1 << 62, nil
	}
	return v, nil
}

// editPRSpan is the edit's presentation duration in nanoseconds.
func editPRSpan(e Edit, timescale uint32) (model.Nanos, error) {
	if e.DurationTicks == 0 {
		return 0, nil
	}
	return model.NanosFromRatio(e.DurationTicks, int64(timescale))
}

// mediaToPR converts media ticks inside edit e to a presentation offset.
func mediaToPR(e Edit, tick int64, timescale uint32) (model.Nanos, error) {
	delta := tick - e.MediaTime
	// presentation ticks = media ticks / rate
	pt, err := model.MulDiv(delta, e.Rate.Den, e.Rate.Num)
	if err != nil {
		return 0, err
	}
	return model.NanosFromRatio(pt, int64(timescale))
}

// SyncReport quantifies audio/video alignment for one version.
type SyncReport struct {
	Asset          string
	Version        string
	OriginSkewMS   int64
	BoundarySkewMS int64
	ErrorMS        int64
}

// SyncError computes the A/V alignment report for a mapped version. The
// reported error is the larger of the origin skew (differences in first
// presented media time before normalisation) and the boundary skew (residual
// differences between the first segment starts after normalisation), so it
// never under-reports a misalignment.
func SyncError(tracks []MappedTrack) SyncReport {
	rep := SyncReport{}
	if len(tracks) == 0 {
		return rep
	}
	rep.Asset = tracks[0].Asset
	rep.Version = tracks[0].Version
	originSkew := model.Nanos(0)
	for _, t := range tracks[1:] {
		d := t.PresentedOrigin - tracks[0].PresentedOrigin
		if d < 0 {
			d = -d
		}
		if d > originSkew {
			originSkew = d
		}
	}
	boundarySkew := model.Nanos(0)
	for i := 1; i < len(tracks); i++ {
		d := tracks[i].Start() - tracks[0].Start()
		if d < 0 {
			d = -d
		}
		if d > boundarySkew {
			boundarySkew = d
		}
	}
	rep.OriginSkewMS = originSkew.Milliseconds()
	rep.BoundarySkewMS = boundarySkew.Milliseconds()
	rep.ErrorMS = rep.OriginSkewMS
	if rep.BoundarySkewMS > rep.ErrorMS {
		rep.ErrorMS = rep.BoundarySkewMS
	}
	return rep
}

// Locate finds the segment containing n and reports the distance from its
// start: a seeking deviation callers can surface.
func (t MappedTrack) Locate(n model.Nanos) (MappedSegment, model.Nanos, bool) {
	i := sort.Search(len(t.Segments), func(i int) bool {
		return t.Segments[i].Timeline.End > n
	})
	if i >= len(t.Segments) {
		return MappedSegment{}, 0, false
	}
	seg := t.Segments[i]
	if !seg.Timeline.Contains(n) {
		return MappedSegment{}, 0, false
	}
	return seg, n - seg.Timeline.Start, true
}

// RTPClock maps RTP timestamps (which wrap) onto the common timeline using the
// payload clock rate and an explicit base pair. It never uses floating point.
type RTPClock struct {
	ClockRate uint32
	BaseRTP   uint32
	BaseTime  model.Nanos
}

// At returns the timeline time of an RTP timestamp, handling 32-bit wrap by
// interpreting the difference as a signed 32-bit delta.
func (c RTPClock) At(ts uint32) (model.Nanos, error) {
	if c.ClockRate == 0 {
		return 0, model.ErrZeroTimescale
	}
	d := int64(int32(ts - c.BaseRTP))
	v, err := model.NanosFromRatio(d, int64(c.ClockRate))
	if err != nil {
		return 0, err
	}
	return c.BaseTime + v, nil
}
