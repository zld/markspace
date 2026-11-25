package timeline

import "fmt"

// EmptyMediaTime is the ISO BMFF sentinel for an empty edit.
const EmptyMediaTime int64 = -1

// Edit is one edit-list entry. SegmentDuration is in MovieTimescale ticks;
// MediaTime is in MediaTimescale ticks and may be EmptyMediaTime.
type Edit struct {
	SegmentDuration int64 `json:"segment_duration"`
	MediaTime       int64 `json:"media_time"`
}

// EditList maps a movie presentation timeline to one track's media timeline.
type EditList struct {
	MovieTimescale uint32 `json:"movie_timescale"`
	MediaTimescale uint32 `json:"media_timescale"`
	Edits          []Edit `json:"edits"`
}

// EditSpan is a located edit in exact presentation seconds.
type EditSpan struct {
	Index     int      `json:"index"`
	Start     Rational `json:"start"`
	Duration  Rational `json:"duration"`
	MediaTime Rational `json:"media_time"`
	Empty     bool     `json:"empty"`
}

// Spans returns exact presentation spans in edit order.
func (l EditList) Spans() ([]EditSpan, error) {
	if l.MovieTimescale == 0 || l.MediaTimescale == 0 {
		return nil, fmt.Errorf("edit list: zero timescale")
	}
	cursor := Rat(0, 1)
	out := make([]EditSpan, 0, len(l.Edits))
	for i, e := range l.Edits {
		if e.SegmentDuration <= 0 {
			return nil, fmt.Errorf("edit list: edit %d has non-positive duration %d", i, e.SegmentDuration)
		}
		dur := Rat(e.SegmentDuration, int64(l.MovieTimescale))
		span := EditSpan{Index: i, Start: cursor, Duration: dur}
		if e.MediaTime == EmptyMediaTime {
			span.Empty = true
		} else if e.MediaTime < 0 {
			return nil, fmt.Errorf("edit list: edit %d has invalid media_time %d", i, e.MediaTime)
		} else {
			span.MediaTime = Rat(e.MediaTime, int64(l.MediaTimescale))
		}
		out = append(out, span)
		cursor = cursor.Add(dur)
	}
	return out, nil
}

// Mapping is the result of mapping one presentation instant.
type Mapping struct {
	Presentation Rational `json:"presentation"`
	Media        Rational `json:"media"`
	EditIndex    int      `json:"edit_index"`
	InGap        bool     `json:"in_gap"`
}

// MapPresentation maps presentation seconds to media seconds. Empty edits are
// returned as gaps rather than being collapsed away.
func (l EditList) MapPresentation(presentation Rational) (Mapping, error) {
	spans, err := l.Spans()
	if err != nil {
		return Mapping{}, err
	}
	if presentation.Cmp(Rat(0, 1)) < 0 {
		return Mapping{}, fmt.Errorf("edit list: negative presentation time %s", presentation)
	}
	for _, s := range spans {
		end := s.Start.Add(s.Duration)
		if presentation.Cmp(s.Start) >= 0 && presentation.Cmp(end) < 0 {
			if s.Empty {
				return Mapping{Presentation: presentation, EditIndex: s.Index, InGap: true}, nil
			}
			offset := presentation.Sub(s.Start)
			return Mapping{
				Presentation: presentation,
				Media:        s.MediaTime.Add(offset),
				EditIndex:    s.Index,
			}, nil
		}
	}
	if len(spans) > 0 && presentation.Cmp(spans[len(spans)-1].Start.Add(spans[len(spans)-1].Duration)) == 0 {
		s := spans[len(spans)-1]
		if s.Empty {
			return Mapping{Presentation: presentation, EditIndex: s.Index, InGap: true}, nil
		}
		return Mapping{Presentation: presentation, Media: s.MediaTime.Add(s.Duration), EditIndex: s.Index}, nil
	}
	return Mapping{}, fmt.Errorf("edit list: presentation time %s outside edits", presentation)
}

// RangeIssue identifies an edit that exceeds the media sample table.
type RangeIssue struct {
	EditIndex     int   `json:"edit_index"`
	MediaTime     int64 `json:"media_time"`
	MediaDuration int64 `json:"media_duration"`
	RequiredEnd   int64 `json:"required_end"`
	OverflowTicks int64 `json:"overflow_ticks"`
}

// ValidateMediaRange checks non-empty edits against the exact media duration.
func (l EditList) ValidateMediaRange(mediaDurationTicks int64) []RangeIssue {
	var issues []RangeIssue
	for i, e := range l.Edits {
		if e.MediaTime == EmptyMediaTime {
			continue
		}
		durationMediaTicks := e.SegmentDuration * int64(l.MediaTimescale) / int64(l.MovieTimescale)
		required := e.MediaTime + durationMediaTicks
		if required > mediaDurationTicks {
			issues = append(issues, RangeIssue{
				EditIndex: i, MediaTime: e.MediaTime, MediaDuration: mediaDurationTicks,
				RequiredEnd: required, OverflowTicks: required - mediaDurationTicks,
			})
		}
	}
	return issues
}
