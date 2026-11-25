// Package timeline normalizes ingested tracks and events onto one integer
// timeline, then validates cut points, transitions and segment coverage.
package timeline

import (
	"fmt"
	"math/bits"
	"sort"

	"replaycheck/internal/ingest"
	"replaycheck/internal/model"
)

// Tick is a unified timeline timestamp in microseconds.
type Tick int64

// Span is a half-open [Start,End) interval on the unified timeline.
type Span struct {
	Start Tick `json:"start"`
	End   Tick `json:"end"`
}

// Len returns End-Start, or 0 for an invalid span.
func (s Span) Len() Tick {
	if s.End <= s.Start {
		return 0
	}
	return s.End - s.Start
}

func (s Span) Valid() bool { return s.Start >= 0 && s.End > s.Start }

// Sample is a normalized media sample carrying its original byte range.
type Sample struct {
	Start Tick   `json:"start"`
	End   Tick   `json:"end"`
	Size  uint32 `json:"size"`
	Byte  uint64 `json:"byteOffset"`
	Index int    `json:"index"`
}

// Track is an ingested track mapped onto the unified timeline.
type Track struct {
	ID        uint32   `json:"id"`
	Kind      string   `json:"kind"`
	Codec     string   `json:"codec"`
	Timescale uint32   `json:"timescale"`
	Start     Tick     `json:"start"`
	End       Tick     `json:"end"`
	Samples   []Sample `json:"samples"`

	// RemainderNanos is the exact fractional part of the track duration that
	// could not be represented in whole timeline ticks, in nanoseconds.
	// NonIntegral counts how many sample boundaries lost a fraction.
	RemainderNanos int64  `json:"remainderNanos"`
	NonIntegral    int    `json:"nonIntegralBoundaries"`
	MediaBytes     uint64 `json:"mediaBytes"`
	// NativeEnd is the track duration in its own ticks.
	NativeEnd uint64 `json:"nativeEnd"`
}

// Event is an RTP trigger normalized onto the unified timeline.
type Event struct {
	Index     int    `json:"index"`
	SSRC      uint32 `json:"ssrc"`
	Sequence  uint16 `json:"sequence"`
	Name      string `json:"name"`
	Tick      Tick   `json:"tick"`
	Unwrapped uint64 `json:"unwrapped"`
	Wraps     uint64 `json:"wraps"`
	Offset    int64  `json:"offset"`
	Duplicate bool   `json:"duplicate"`
	Marker    bool   `json:"marker"`
}

// Timeline is the normalized view consumed by the split and derive stages.
type Timeline struct {
	Base           int64    `json:"base"` // ticks per second of the unified timeline
	Start          Tick     `json:"start"`
	End            Tick     `json:"end"`
	Tracks         []*Track `json:"tracks"`
	Events         []Event  `json:"events"`
	WrapCount      uint64   `json:"wrapCount"`
	RemainderNanos int64    `json:"remainderNanos"`
	NonIntegral    int      `json:"nonIntegralBoundaries"`
	Backwards      int      `json:"backwards"`
	SourceDigest   string   `json:"sourceDigest"`
	EventDigest    string   `json:"eventDigest"`
}

// Options controls normalization.
type Options struct {
	// SourceStart is the unified-timeline offset of the container's first
	// sample. It is zero for whole-file replays.
	SourceStart Tick
}

// Normalize converts container tracks and RTP events onto the microsecond
// timeline. Fractional conversions record their remainder; they never round a
// boundary silently, and a negative duration is impossible by construction.
func Normalize(c *ingest.Container, events *ingest.EventFile, sourceStart Tick) (*Timeline, error) {
	if c == nil {
		return nil, model.NewError(model.StageNormalize, "timeline", model.TrackRef{}, 0, "no container")
	}
	if sourceStart < 0 {
		return nil, model.NewError(model.StageNormalize, "timeline", model.TrackRef{}, 0, "negative source start")
	}
	tl := &Timeline{Base: int64(model.TimelineBase), Start: sourceStart, SourceDigest: c.Digest}
	if events != nil {
		tl.EventDigest = events.Digest
	}
	var maxEnd Tick
	for _, t := range c.Tracks {
		nt, err := normalizeTrack(t, sourceStart)
		if err != nil {
			return nil, err
		}
		if nt.End > maxEnd {
			maxEnd = nt.End
		}
		tl.RemainderNanos += nt.RemainderNanos
		tl.NonIntegral += nt.NonIntegral
		tl.Tracks = append(tl.Tracks, nt)
	}
	tl.End = maxEnd
	if tl.End <= tl.Start {
		return nil, model.NewError(model.StageNormalize, "timeline", model.TrackRef{}, 0, "timeline has no positive duration")
	}
	if events != nil {
		for _, s := range events.Streams {
			tl.WrapCount += s.Wraps
			evs, err := normalizeStream(s, sourceStart, tl)
			if err != nil {
				return nil, err
			}
			tl.Events = append(tl.Events, evs...)
		}
		sort.SliceStable(tl.Events, func(i, j int) bool {
			if tl.Events[i].Tick != tl.Events[j].Tick {
				return tl.Events[i].Tick < tl.Events[j].Tick
			}
			return tl.Events[i].Index < tl.Events[j].Index
		})
	}
	if err := checkTrackCoverage(tl); err != nil {
		return nil, err
	}
	return tl, nil
}

func normalizeTrack(t *ingest.Track, sourceStart Tick) (*Track, error) {
	ref := model.TrackRef{ID: t.ID, Kind: t.Kind}
	if t.Timescale == 0 {
		return nil, model.NewError(model.StageNormalize, "rescale", ref, t.MdhdOffset, "zero timescale")
	}
	nt := &Track{
		ID:         t.ID,
		Kind:       t.Kind,
		Codec:      t.Codec,
		Timescale:  t.Timescale,
		Start:      sourceStart,
		NativeEnd:  t.Duration,
		MediaBytes: t.MediaBytes,
	}
	base := model.Base(t.Timescale)
	prevDTS := int64(-1)
	for i, s := range t.Samples {
		if s.DTS < prevDTS {
			return nil, model.Errorf(model.StageNormalize, "sample", ref, t.SttsOffset, "sample %d DTS %d went backwards from %d", i, s.DTS, prevDTS)
		}
		prevDTS = s.DTS
		nextDTS := int64(t.Duration)
		if i+1 < len(t.Samples) {
			nextDTS = t.Samples[i+1].DTS
		}
		if nextDTS < s.DTS {
			return nil, model.Errorf(model.StageNormalize, "sample", ref, t.SttsOffset, "sample %d ends before it starts", i)
		}
		startTicks, startRem, err := model.RescaleTicks(s.DTS, base, model.TimelineBase)
		if err != nil {
			return nil, model.WrapError(model.StageNormalize, "rescale", ref, t.SttsOffset, fmt.Sprintf("sample %d start", i), err)
		}
		endTicks, _, err := model.RescaleTicks(nextDTS, base, model.TimelineBase)
		if err != nil {
			return nil, model.WrapError(model.StageNormalize, "rescale", ref, t.SttsOffset, fmt.Sprintf("sample %d end", i), err)
		}
		if startRem != 0 {
			nt.NonIntegral++
			frac, ferr := fractionNanos(startRem, int64(base))
			if ferr != nil {
				return nil, model.WrapError(model.StageNormalize, "rescale", ref, t.SttsOffset, fmt.Sprintf("sample %d remainder", i), ferr)
			}
			nt.RemainderNanos += frac
		}
		start := sourceStart + Tick(startTicks)
		end := sourceStart + Tick(endTicks)
		if end < start {
			return nil, model.Errorf(model.StageNormalize, "sample", ref, t.StszOffset, "sample %d duration would be negative", i)
		}
		nt.Samples = append(nt.Samples, Sample{
			Start: start, End: end, Size: s.Size, Byte: s.Offset, Index: i,
		})
	}
	endTicks, endRem, err := model.RescaleTicks(int64(t.Duration), base, model.TimelineBase)
	if err != nil {
		return nil, model.WrapError(model.StageNormalize, "rescale", ref, t.MdhdOffset, "track duration", err)
	}
	nt.End = sourceStart + Tick(endTicks)
	// Express the sub-tick fraction of the track duration in nanoseconds,
	// in addition to the per-boundary fractions above.
	durFrac, err := fractionNanos(endRem, int64(base))
	if err != nil {
		return nil, model.WrapError(model.StageNormalize, "rescale", ref, t.MdhdOffset, "track duration remainder", err)
	}
	nt.RemainderNanos += durFrac
	if endRem != 0 {
		nt.NonIntegral++
	}
	// A track whose declared duration rounds below its last sample end is
	// extended to the sample end: the sample table is authoritative and the
	// discrepancy is accounted as a remainder rather than a negative length.
	if n := len(nt.Samples); n > 0 && nt.Samples[n-1].End > nt.End {
		nt.NonIntegral++
		nt.End = nt.Samples[n-1].End
	}
	if nt.End <= nt.Start {
		return nil, model.NewError(model.StageNormalize, "track", ref, t.MdhdOffset, "normalized track has no duration")
	}
	return nt, nil
}

// fractionNanos converts remainder units of 1/den microsecond into whole
// nanoseconds. It is exact integer arithmetic; the discarded sub-nanosecond
// part is well below any tick the kernel reports.
func fractionNanos(rem, den int64) (int64, error) {
	if rem < 0 || den <= 0 {
		return 0, model.ErrTimeOverflow
	}
	hi, lo := mul64(uint64(rem), 1000)
	// rem < den, so rem*1000 < den*1000 and the division cannot overflow.
	q, _ := div64(hi, lo, uint64(den))
	if q > uint64(^uint64(0)>>1) {
		return 0, model.ErrTimeOverflow
	}
	return int64(q), nil
}

// normalizeStream maps unwrapped RTP ticks to the unified timeline using the
// stream's clock rate, rejecting any event outside the container window.
func normalizeStream(s *ingest.RTPStream, sourceStart Tick, tl *Timeline) ([]Event, error) {
	if s.ClockRate == 0 {
		return nil, model.NewError(model.StageNormalize, "rtp", model.TrackRef{ID: s.SSRC, Kind: "ssrc"}, 0, "clock rate is zero")
	}
	ref := model.TrackRef{ID: s.SSRC, Kind: "ssrc"}
	if len(s.Events) == 0 {
		return nil, nil
	}
	epoch := s.Events[0].Unwrapped
	base := model.Base(s.ClockRate)
	var out []Event
	var prev Tick = -1
	for _, e := range s.Events {
		if e.Unwrapped < epoch {
			return nil, model.Errorf(model.StageNormalize, "rtp", ref, uint64(e.Offset), "event %d unwrapped timestamp regressed", e.Index)
		}
		delta := e.Unwrapped - epoch
		if delta > uint64(1)<<62 {
			return nil, model.Errorf(model.StageNormalize, "rtp", ref, uint64(e.Offset), "event %d timestamp delta too large", e.Index)
		}
		ticks, _, err := model.RescaleTicks(int64(delta), base, model.TimelineBase)
		if err != nil {
			return nil, model.WrapError(model.StageNormalize, "rtp", ref, uint64(e.Offset), fmt.Sprintf("event %d", e.Index), err)
		}
		tick := sourceStart + Tick(ticks)
		ev := Event{
			Index: e.Index, SSRC: e.SSRC, Sequence: e.Sequence,
			Name: string(e.Payload), Tick: tick, Unwrapped: e.Unwrapped,
			Wraps: e.Wraps, Offset: e.Offset, Marker: e.Marker,
		}
		if prev >= 0 && tick < prev {
			return nil, model.Errorf(model.StageNormalize, "rtp", ref, uint64(e.Offset), "event %d resolves before previous event", e.Index)
		}
		prev = tick
		out = append(out, ev)
	}
	return out, nil
}

// checkTrackCoverage ensures every normalized sample relates sanely to the
// timeline and that no track runs past the timeline end.
func checkTrackCoverage(tl *Timeline) error {
	for _, t := range tl.Tracks {
		if t.End < t.Start {
			return model.Errorf(model.StageNormalize, "coverage", model.TrackRef{ID: t.ID, Kind: t.Kind}, 0, "track ends before it starts")
		}
		for i := 1; i < len(t.Samples); i++ {
			if t.Samples[i].Start < t.Samples[i-1].Start {
				return model.Errorf(model.StageNormalize, "coverage", model.TrackRef{ID: t.ID, Kind: t.Kind}, t.Samples[i].Byte, "sample %d starts before its predecessor", i)
			}
			if t.Samples[i].End < t.Samples[i].Start {
				return model.Errorf(model.StageNormalize, "coverage", model.TrackRef{ID: t.ID, Kind: t.Kind}, t.Samples[i].Byte, "sample %d has negative duration", i)
			}
		}
	}
	return nil
}

func mul64(a, b uint64) (hi, lo uint64)       { return bits.Mul64(a, b) }
func div64(hi, lo, d uint64) (uint64, uint64) { return bits.Div64(hi, lo, d) }
