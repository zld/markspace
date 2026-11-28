// Package timeline merges parsed tracks onto one nanosecond timeline and
// produces the sample spans every downstream stage works from.
package timeline

import (
	"sort"

	"mediaregress/internal/media"
	"mediaregress/internal/track"
)

// TrackTimeline is the resolved view of one track.
type TrackTimeline struct {
	TrackID          string                `json:"track"`
	Kind             track.Kind            `json:"kind"`
	Container        string                `json:"container"`
	Timescale        uint32                `json:"timescale"`
	Start            media.Ticks           `json:"start_ns"`
	End              media.Ticks           `json:"end_ns"`
	Duration         media.Ticks           `json:"duration_ns"`
	DeclaredDuration media.Ticks           `json:"declared_duration_ns"`
	Samples          []track.Sample        `json:"samples"`
	Metadata         []MetaOrigin          `json:"metadata"`
	Rotation         int                   `json:"rotation_deg"`
	Crop             *track.Aperture       `json:"crop,omitempty"`
	Colors           []ColorSummary        `json:"colors,omitempty"`
	Conflicts        []track.ColorConflict `json:"color_conflicts,omitempty"`
	Truncated        bool                  `json:"truncated,omitempty"`
	TruncatedAt      media.Ticks           `json:"truncated_at_ns,omitempty"`
	TruncatedOffset  int64                 `json:"truncated_offset,omitempty"`
	Rejected         bool                  `json:"rejected,omitempty"`
	Errors           []*media.Error        `json:"errors,omitempty"`
	ParseVersion     string                `json:"parse_version"`
}

// MetaOrigin records where a timeline value came from, so every merged field
// can be traced back to a container element and byte offset.
type MetaOrigin struct {
	Field   string       `json:"field"`
	Value   string       `json:"value"`
	Source  track.Source `json:"source"`
	Version string       `json:"parse_version"`
}

// ColorSummary is the timeline-level view of a color description. Raw values
// are carried through unchanged.
type ColorSummary struct {
	Primaries         uint16       `json:"primaries"`
	Transfer          uint16       `json:"transfer"`
	Matrix            uint16       `json:"matrix"`
	FullRange         bool         `json:"full_range"`
	Raw               string       `json:"raw"`
	Source            track.Source `json:"source"`
	Interpretation    string       `json:"interpretation"`
	RequiresRendering bool         `json:"requires_rendering"`
}

// Timeline is the merged, ordered set of per-track timelines.
type Timeline struct {
	Start    media.Ticks      `json:"start_ns"`
	End      media.Ticks      `json:"end_ns"`
	Tracks   []*TrackTimeline `json:"tracks"`
	Rejected []*TrackTimeline `json:"rejected_tracks,omitempty"`
	Errors   []*media.Error   `json:"errors,omitempty"`
	Version  string           `json:"parse_version"`
}

// Track returns the timeline for a track ID.
func (tl *Timeline) Track(id string) *TrackTimeline {
	for _, t := range tl.Tracks {
		if t.TrackID == id {
			return t
		}
	}
	return nil
}

// Build merges all tracks onto one timeline. It never interpolates or rescales:
// track times were already placed on the shared scale by the parser, so a
// backwards or out-of-range span rejects the track and records the input
// offset of the offending sample.
func Build(in *track.Input) (*Timeline, error) {
	if in == nil {
		return nil, media.NewError(media.ClassDependency, media.StageTimeline, "", -1, "no parsed input")
	}
	tl := &Timeline{Version: track.ParseVersion}
	for _, tr := range in.Tracks {
		tt := buildTrack(tr)
		if tt.Rejected {
			tl.Rejected = append(tl.Rejected, tt)
			for _, e := range tt.Errors {
				tl.Errors = append(tl.Errors, e)
			}
			continue
		}
		tl.Tracks = append(tl.Tracks, tt)
		if tt.End > tl.End {
			tl.End = tt.End
		}
	}
	sort.Slice(tl.Tracks, func(i, j int) bool { return tl.Tracks[i].TrackID < tl.Tracks[j].TrackID })
	sort.Slice(tl.Rejected, func(i, j int) bool { return tl.Rejected[i].TrackID < tl.Rejected[j].TrackID })
	if len(tl.Tracks) == 0 {
		return tl, media.NewError(media.ClassDependency, media.StageTimeline, "", -1,
			"no usable tracks after timeline build")
	}
	return tl, nil
}

func buildTrack(tr *track.Track) *TrackTimeline {
	tt := &TrackTimeline{
		TrackID:         tr.ID,
		Kind:            tr.Kind,
		Container:       tr.Container,
		Timescale:       tr.Timescale,
		Rotation:        tr.Rotation,
		Crop:            tr.Crop,
		Conflicts:       tr.Conflicts,
		Truncated:       tr.Truncated,
		TruncatedAt:     tr.TruncatedAt,
		TruncatedOffset: tr.TruncatedOffset,
		Rejected:        tr.Rejected,
		ParseVersion:    tr.Version,
	}
	// Carry over parse errors with their original classes and offsets.
	tt.Errors = append(tt.Errors, tr.Errors...)
	if tr.Timescale == 0 {
		tt.Rejected = true
		tt.Errors = append(tt.Errors, media.NewError(media.ClassInvalidTimebase, media.StageTimeline,
			tr.ID, tr.SourceStart, "track timescale is zero"))
		return tt
	}
	var prevEnd media.Ticks
	for _, s := range tr.Samples {
		if s.Duration < 0 || s.Start < 0 {
			tt.Rejected = true
			tt.Errors = append(tt.Errors, media.Errorf(media.ClassTimeRegression, media.StageTimeline,
				tr.ID, s.Offset, "sample %d has a negative span", s.Index))
			return tt
		}
		if s.Start < prevEnd {
			tt.Rejected = true
			tt.Errors = append(tt.Errors, media.Errorf(media.ClassTimeRegression, media.StageTimeline,
				tr.ID, s.Offset, "sample %d starts before the previous sample ends", s.Index))
			return tt
		}
		prevEnd = s.End()
	}
	tt.Samples = tr.Samples
	if len(tr.Samples) > 0 {
		tt.Start = tr.Samples[0].Start
		tt.End = tr.Samples[len(tr.Samples)-1].End()
	}
	tt.Duration = tt.End - tt.Start
	tt.DeclaredDuration = tr.DurationOrEnd()
	if d := tr.DurationOrEnd(); d > tt.End && !tt.Truncated {
		// Declared duration longer than the sample table: keep the sample
		// boundary as authoritative but record the disagreement. A truncated
		// track is expected to fall short, and its truncation error already
		// names the affected interval.
		tt.Errors = append(tt.Errors, media.Errorf(media.ClassCorruptInput, media.StageTimeline,
			tr.ID, tr.SourceStart, "declared duration %d exceeds sample end %d", d, tt.End))
	}
	tt.Metadata = append(tt.Metadata,
		MetaOrigin{Field: "rotation", Value: itoa(int64(tr.Rotation)), Source: tr.RotSource, Version: tr.Version},
		MetaOrigin{Field: "timescale", Value: itoa(int64(tr.Timescale)),
			Source:  track.Source{Container: tr.Container, Element: "mdhd/timescale", Offset: tr.SourceStart},
			Version: tr.Version},
	)
	if tr.Crop != nil {
		tt.Metadata = append(tt.Metadata, MetaOrigin{
			Field: "crop", Value: itoa(tr.Crop.Left) + "," + itoa(tr.Crop.Top) + "," +
				itoa(tr.Crop.Width) + "," + itoa(tr.Crop.Height),
			Source: tr.Crop.Source, Version: tr.Version,
		})
	}
	for _, c := range tr.Colors {
		tt.Colors = append(tt.Colors, ColorSummary{
			Primaries: c.Primaries, Transfer: c.Transfer, Matrix: c.Matrix,
			FullRange: c.FullRange, Raw: c.Raw, Source: c.Source,
			Interpretation: c.Interpretation, RequiresRendering: c.RequiresRendering,
		})
		tt.Metadata = append(tt.Metadata, MetaOrigin{
			Field: "color", Value: c.Raw, Source: c.Source, Version: tr.Version,
		})
	}
	return tt
}

// SampleAt resolves an instant to the containing sample on a track timeline.
func (tt *TrackTimeline) SampleAt(at media.Ticks) (track.Sample, bool) {
	i := sort.Search(len(tt.Samples), func(i int) bool { return tt.Samples[i].End() > at })
	if i < len(tt.Samples) && tt.Samples[i].Start <= at {
		return tt.Samples[i], true
	}
	return track.Sample{}, false
}

// SampleBoundaries returns the ordered distinct sample starts inside [start,end).
func (tt *TrackTimeline) SampleBoundaries(start, end media.Ticks) []media.Ticks {
	var out []media.Ticks
	for _, s := range tt.Samples {
		if s.End() <= start {
			continue
		}
		if s.Start >= end {
			break
		}
		b := s.Start
		if b < start {
			b = start
		}
		if len(out) == 0 || out[len(out)-1] != b {
			out = append(out, b)
		}
	}
	return out
}

// sampleOffset keeps a positioned error even for synthetic timelines whose
// samples carry no file offset.
func sampleOffset(s track.Sample) int64 {
	if s.Offset < 0 {
		return -1
	}
	return s.Offset
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ErrorsByTrack returns timeline errors grouped by track, in track order.
func (tl *Timeline) ErrorsByTrack() map[string][]*media.Error {
	out := map[string][]*media.Error{}
	for _, e := range tl.Errors {
		out[e.Track] = append(out[e.Track], e)
	}
	return out
}
