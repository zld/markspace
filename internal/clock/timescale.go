package clock

import (
	"fmt"
	"math/bits"
)

// EventTime is nanoseconds on the shared program/event timeline.
type EventTime int64

// MediaTime is a possibly non-zero timestamp in a track's own timescale.
type MediaTime int64

// Nanosecond is the common EventTime tick.
const Nanosecond EventTime = 1

// TicksPerSecond describes a media clock.
type TicksPerSecond uint32

// NS converts clock ticks to integer nanoseconds using floor division.
func (s TicksPerSecond) NS(ticks int64) EventTime {
	if s == 0 {
		return 0
	}
	return EventTime(mulDivFloor(ticks, 1_000_000_000, int64(s)))
}

// FromNS converts event nanoseconds to clock ticks using floor division.
func (s TicksPerSecond) FromNS(ns EventTime) int64 {
	if s == 0 {
		return 0
	}
	return mulDivFloor(int64(ns), int64(s), 1_000_000_000)
}

// Validate rejects a missing or incompatible timescale.
func (s TicksPerSecond) Validate(path string, track int) error {
	if s == 0 {
		return New(CategoryTimebase, path, track, 0, "timescale must be positive")
	}
	return nil
}

// EditEntry is one decoded edts/elst entry. Duration is expressed in the
// movie timescale. MediaTime is in the media timescale; -1 denotes an empty edit.
type EditEntry struct {
	Duration  int64 `json:"duration"`
	MediaTime int64 `json:"media_time"`
	Offset    int64 `json:"raw_offset"`
}

type segment struct {
	eventStart EventTime
	eventEnd   EventTime
	mediaStart MediaTime
	mediaEnd   MediaTime
	scale      TicksPerSecond
	empty      bool
}

// TimeMap maps one track between the common event clock and its media clock.
type TimeMap struct {
	Path       string
	Track      int
	MovieScale TicksPerSecond
	MediaScale TicksPerSecond
	segments   []segment
}

// NewIdentityMap creates a map for a track with no edit list. MediaStart is
// normally the first unwrapped RTP timestamp or the first MP4 composition time.
func NewIdentityMap(path string, track int, scale TicksPerSecond, mediaStart MediaTime, durationTicks int64) (*TimeMap, error) {
	if err := scale.Validate(path, track); err != nil {
		return nil, err
	}
	if durationTicks < 0 {
		return nil, New(CategoryInvalidArgument, path, track, 0, "negative media duration")
	}
	end := mediaStart + MediaTime(durationTicks)
	return &TimeMap{
		Path:       path,
		Track:      track,
		MovieScale: scale,
		MediaScale: scale,
		segments: []segment{{
			eventStart: 0,
			eventEnd:   scale.NS(durationTicks),
			mediaStart: mediaStart,
			mediaEnd:   end,
			scale:      scale,
		}},
	}, nil
}

// BuildEditMap resolves edit-list gaps and media offsets into ordered mappings.
func BuildEditMap(path string, track int, movieScale, mediaScale TicksPerSecond, entries []EditEntry) (*TimeMap, error) {
	if err := movieScale.Validate(path, track); err != nil {
		return nil, err
	}
	if err := mediaScale.Validate(path, track); err != nil {
		return nil, err
	}
	tm := &TimeMap{Path: path, Track: track, MovieScale: movieScale, MediaScale: mediaScale}
	var cursor EventTime
	for i, e := range entries {
		if e.Offset < 0 {
			e.Offset = 0
		}
		if e.Duration < 0 {
			return nil, New(CategoryNegativeDuration, path, track, e.Offset,
				fmt.Sprintf("edit %d has negative segment duration %d", i, e.Duration))
		}
		if e.Duration == 0 {
			return nil, New(CategoryInvalidEditList, path, track, e.Offset,
				fmt.Sprintf("edit %d has zero segment duration", i))
		}
		durNS := movieScale.NS(e.Duration)
		if durNS <= 0 {
			return nil, New(CategoryInvalidEditList, path, track, e.Offset,
				fmt.Sprintf("edit %d duration resolves to no nanoseconds", i))
		}
		seg := segment{eventStart: cursor, eventEnd: cursor + durNS, scale: mediaScale}
		if e.MediaTime == -1 {
			seg.empty = true
		} else {
			if e.MediaTime < 0 {
				return nil, New(CategoryInvalidEditList, path, track, e.Offset,
					fmt.Sprintf("edit %d has invalid media_time %d", i, e.MediaTime))
			}
			mediaDur := mulDivFloor(e.Duration, int64(mediaScale), int64(movieScale))
			seg.mediaStart = MediaTime(e.MediaTime)
			seg.mediaEnd = MediaTime(e.MediaTime + mediaDur)
		}
		tm.segments = append(tm.segments, seg)
		cursor = seg.eventEnd
	}
	if len(tm.segments) == 0 {
		return nil, New(CategoryInvalidEditList, path, track, 0, "edit list contains no segments")
	}
	return tm, nil
}

// EventToMedia maps event time to media time. ok is false inside a legal empty edit.
func (m *TimeMap) EventToMedia(t EventTime) (media MediaTime, ok bool, err error) {
	for _, s := range m.segments {
		if t >= s.eventStart && t < s.eventEnd {
			if s.empty {
				return 0, false, nil
			}
			delta := s.scale.FromNS(t - s.eventStart)
			return s.mediaStart + MediaTime(delta), true, nil
		}
	}
	return 0, false, New(CategoryOutOfRange, m.Path, m.Track, 0,
		fmt.Sprintf("event time %d is outside edit timeline", t))
}

// MediaToEvent is the inverse mapping used by random-access indexing.
func (m *TimeMap) MediaToEvent(media MediaTime) (EventTime, error) {
	for _, s := range m.segments {
		if s.empty {
			continue
		}
		if media >= s.mediaStart && media < s.mediaEnd {
			return s.eventStart + s.scale.NS(int64(media-s.mediaStart)), nil
		}
	}
	return 0, New(CategoryOutOfRange, m.Path, m.Track, 0,
		fmt.Sprintf("media time %d is outside mapped edit segments", media))
}

// Span returns the complete event timeline span, including empty edits.
func (m *TimeMap) Span() (EventTime, EventTime) {
	if len(m.segments) == 0 {
		return 0, 0
	}
	return m.segments[0].eventStart, m.segments[len(m.segments)-1].eventEnd
}

// RoundTripError returns the absolute track-tick error for media->event->media.
func (m *TimeMap) RoundTripError(media MediaTime) (int64, error) {
	ev, err := m.MediaToEvent(media)
	if err != nil {
		return 0, err
	}
	back, ok, err := m.EventToMedia(ev)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, New(CategoryInvalidEditList, m.Path, m.Track, 0, "round trip landed in empty edit")
	}
	d := int64(back - media)
	if d < 0 {
		d = -d
	}
	return d, nil
}

func mulDivFloor(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	neg := (a < 0) != (b < 0)
	ua := abs64(a)
	ub := abs64(b)
	hi, lo := bits.Mul64(ua, ub)
	if hi != 0 && hi >= uint64(c) {
		// Media durations accepted by this kernel cannot reach this range.
		if neg {
			return mathMinInt64
		}
		return mathMaxInt64
	}
	q, _ := bits.Div64(hi, lo, uint64(c))
	if neg && q > 1<<63 {
		return mathMinInt64
	}
	if !neg && q > mathMaxInt64U {
		return mathMaxInt64
	}
	if neg {
		return -int64(q)
	}
	return int64(q)
}

const (
	mathMaxInt64  = int64(^uint64(0) >> 1)
	mathMinInt64  = -mathMaxInt64 - 1
	mathMaxInt64U = uint64(mathMaxInt64)
)

func abs64(v int64) uint64 {
	if v >= 0 {
		return uint64(v)
	}
	if v == mathMinInt64 {
		return 1 << 63
	}
	return uint64(-v)
}
