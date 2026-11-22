// Package clock maps per-track media clocks onto one unified presentation
// timeline. Container tracks use independent timescales, decode timestamps may
// carry a composition offset, and edit lists shift media into presentation
// time. All three are folded into a single tick domain so downstream stages
// (indexing, segmentation, fingerprinting) never see track-local units.
package clock

import (
	"fmt"
	"math"
	"math/bits"

	"github.com/pairwise/mediad/internal/fault"
)

// NanosPerSecond is the unified tick rate used by the pipeline.
const NanosPerSecond uint64 = 1_000_000_000

// Timeline is a conversion context between a source timescale and the unified
// nanosecond domain.
type Timeline struct {
	ticksPerSecond uint64
}

// NewTimeline validates the unified tick rate.
func NewTimeline(ticksPerSecond uint64) (*Timeline, error) {
	if ticksPerSecond == 0 {
		return nil, fault.Errorf("clock.timeline", fault.KindTimeline, "zero tick rate")
	}
	return &Timeline{ticksPerSecond: ticksPerSecond}, nil
}

// NanosecondTimeline returns the pipeline default timeline.
func NanosecondTimeline() *Timeline { return &Timeline{ticksPerSecond: NanosPerSecond} }

// TicksPerSecond reports the unified tick rate.
func (tl *Timeline) TicksPerSecond() uint64 { return tl.ticksPerSecond }

// Convert rescales a source tick value into unified ticks, rounding to the
// nearest tick and reporting overflow instead of silently wrapping.
func (tl *Timeline) Convert(value uint64, sourceScale uint32) (int64, error) {
	if sourceScale == 0 {
		return 0, fault.Errorf("clock.convert", fault.KindTimeline, "zero source timescale")
	}
	hi, lo := bits.Mul64(value, tl.ticksPerSecond)
	if hi >= uint64(sourceScale) {
		return 0, fault.Errorf("clock.convert", fault.KindTimeline, "rescale %d/%d overflows int64", value, sourceScale)
	}
	q, r := bits.Div64(hi, lo, uint64(sourceScale))
	if q > math.MaxInt64 {
		return 0, fault.Errorf("clock.convert", fault.KindTimeline, "rescaled %d ticks overflows int64", q)
	}
	// Round half up; r < sourceScale so doubling cannot overflow uint64 here.
	if r*2 >= uint64(sourceScale) {
		q++
		if q > math.MaxInt64 {
			return 0, fault.Errorf("clock.convert", fault.KindTimeline, "rounded value overflows int64")
		}
	}
	return int64(q), nil
}

// Edit is one elst entry: SegmentDuration on the presentation timeline,
// MediaTime on the media timeline (-1 marks an empty edit), and a fixed-point
// 16.16 rate.
type Edit struct {
	SegmentDuration uint64
	MediaTime       int64
	RateFixed       int32 // 0 or 0x00010000 mean 1.0
}

func (e Edit) rate() float64 {
	switch e.RateFixed {
	case 0, 0x00010000:
		return 1
	default:
		return float64(e.RateFixed) / 65536
	}
}

// EditMap maps media ticks to presentation ticks through an edit list.
type EditMap struct {
	edits []Edit
}

// NewEditMap copies entries. An empty list is a passthrough.
func NewEditMap(edits []Edit) *EditMap {
	return &EditMap{edits: append([]Edit(nil), edits...)}
}

// Map translates a media tick position to presentation ticks. When no edit
// list is present the input is returned unchanged.
func (em *EditMap) Map(media int64) (int64, error) {
	if len(em.edits) == 0 {
		return media, nil
	}
	var out int64
	for i, e := range em.edits {
		if e.SegmentDuration == 0 {
			continue
		}
		seg := int64(e.SegmentDuration)
		if e.MediaTime < 0 {
			out += seg
			continue
		}
		rate := e.rate()
		if rate <= 0 {
			return 0, fault.Errorf("clock.edit", fault.KindTimeline, "edit %d has non-positive rate", i)
		}
		end := e.MediaTime + int64(math.Round(float64(seg)*rate))
		if media >= e.MediaTime && media < end {
			delta := int64(math.Round(float64(media-e.MediaTime) / rate))
			return out + delta, nil
		}
		out += seg
	}
	return 0, fault.Errorf("clock.edit", fault.KindTimeline,
		"media tick %d falls outside %d edit segments", media, len(em.edits))
}

// PresentationEnd reports the total presentation duration of the edit list.
func (em *EditMap) PresentationEnd() int64 {
	var out int64
	for _, e := range em.edits {
		if e.SegmentDuration <= math.MaxInt64 {
			out += int64(e.SegmentDuration)
		}
	}
	if len(em.edits) == 0 {
		return 0
	}
	return out
}

// MaxForwardGapNS bounds the jump between consecutive decode timestamps.
// Decode timestamps inside one track are contiguous by construction, so a
// multi-minute leap means the clock wrapped past the measurable range and the
// low bits alone cannot be trusted.
const MaxForwardGapNS int64 = 5 * 60 * 1_000_000_000

// TrackClock converts one track's decode timestamps to presentation time and
// enforces monotonicity. Any wrap, regression or overflow yields a located
// error naming the track and sample.
type TrackClock struct {
	group     string
	track     uint32
	scale     uint32
	tl        *Timeline
	edits     *EditMap
	prev      uint64
	prevMedia int64
	seen      int
	offset    int64 // first mapped DTS, used to anchor tracks at zero
	broke     bool  // a wrap, regression or overflow was observed
}

// NewTrackClock builds a clock for a track. The offset anchors the earliest
// presentation tick at zero so independently clocked tracks align.
func NewTrackClock(group string, trackID uint32, mediaScale uint32, tl *Timeline, edits []Edit, offset int64) (*TrackClock, error) {
	if tl == nil {
		tl = NanosecondTimeline()
	}
	if mediaScale == 0 {
		return nil, fault.Errorf("clock.track", fault.KindTimeline, "zero media timescale").
			WithPath(group).WithTrack(trackID)
	}
	return &TrackClock{
		group: group, track: trackID, scale: mediaScale, tl: tl,
		edits: NewEditMap(edits), offset: offset,
	}, nil
}

// TrackID reports the bound track.
func (c *TrackClock) TrackID() uint32 { return c.track }

// TrackScale reports the media timescale.
func (c *TrackClock) TrackScale() uint32 { return c.scale }

// Presentation maps a decode timestamp plus composition offset to unified
// presentation ticks. It validates ordering before mapping.
func (c *TrackClock) Presentation(dts uint64, compositionOffset int64) (int64, error) {
	if err := c.checkDTS(dts); err != nil {
		return 0, err
	}
	cts := dts
	if compositionOffset >= 0 {
		if uint64(compositionOffset) > math.MaxUint64-dts {
			return 0, c.err(fault.ErrTimeOverflow, c.seen, "composition offset overflows dts %d", dts)
		}
		cts += uint64(compositionOffset)
	} else {
		neg := uint64(-compositionOffset)
		if neg > dts {
			return 0, c.err(fault.ErrTimeOverflow, c.seen, "composition offset underflows dts %d", dts)
		}
		cts -= neg
	}
	media, err := c.tl.Convert(cts, c.scale)
	if err != nil {
		return 0, c.err(fault.ErrTimeOverflow, c.seen, "%v", err)
	}
	if c.seen > 1 && media-c.prevMedia > MaxForwardGapNS {
		return 0, c.err(fault.ErrTimeWrap, c.seen,
			"decode jump of %d ns from %d to %d exceeds the %d ns continuity bound",
			media-c.prevMedia, c.prevMedia, media, MaxForwardGapNS)
	}
	c.prevMedia = media
	presented, err := c.edits.Map(media)
	if err != nil {
		return 0, c.err(fault.ErrBadSample, c.seen, "%v", err)
	}
	presented -= c.offset
	if presented < 0 {
		return 0, c.err(fault.ErrTimeBackwards, c.seen, "presentation time %d precedes timeline origin by %d", presented, -presented)
	}
	return presented, nil
}

func (c *TrackClock) checkDTS(dts uint64) error {
	if c.seen > 0 {
		switch {
		case dts < c.prev:
			// A 32-bit domain rollover presents as a large drop from the top
			// half of the space to the bottom half.
			// Masking a 32-bit clock to its low bits turns a forward past 2^32
			// into a drop of at least 2^30 from the upper half. Larger drops
			// are unmistakable; see the 2^31 case below.
			if c.prev >= 1<<31 && dts < 1<<31 && c.prev-dts >= 1<<30 {
				return c.err(fault.ErrTimeWrap, c.seen, "dts %d followed by %d indicates 32-bit wrap", c.prev, dts)
			}
			return c.err(fault.ErrTimeBackwards, c.seen, "dts %d follows %d", dts, c.prev)
		}
	}
	c.prev = dts
	c.seen++
	return nil
}

func (c *TrackClock) err(cause error, sample int, format string, args ...any) *fault.Error {
	if cause == fault.ErrTimeWrap || cause == fault.ErrTimeBackwards || cause == fault.ErrTimeOverflow {
		c.broke = true
	}
	return fault.Errorf("clock.track", fault.KindTimeline, format, args...).
		WithPath(c.group).WithTrack(c.track).WithSample(sample).Wrapped(cause)
}

// WrapDetected reports whether a wrap or regression was ever observed.
func (c *TrackClock) WrapDetected() bool { return c.broke }

// DecodeEnd returns the last accepted input dts for diagnostics.
func (c *TrackClock) DecodeEnd() uint64 { return c.prev }

// Offset returns the presentation anchor subtracted from mapped values.
func (c *TrackClock) Offset() int64 { return c.offset }

// String implements fmt.Stringer for diagnostics.
func (c *TrackClock) String() string {
	return fmt.Sprintf("track %d @%d/%d", c.track, c.scale, c.tl.TicksPerSecond())
}
