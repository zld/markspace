// Package model defines the unified nanosecond timeline shared by ingestion,
// recovery, segmentation, audio analysis and fingerprint mappings.
package model

import (
	"math/big"

	"github.com/zld/mediacore/internal/trace"
)

// NS is a number of nanoseconds on the common timeline.
type NS int64

// NSPerSecond is the unified time base.
const NSPerSecond = NS(1_000_000_000)

// TicksToNS converts a track-local tick count using timescale ticks/second.
func TicksToNS(ticks uint64, timescale uint32) (NS, error) {
	if timescale == 0 {
		return 0, trace.E(trace.CodeInvalidArgument, trace.StageNormalize, "timescale must not be zero")
	}
	num := new(big.Int).Mul(new(big.Int).SetUint64(ticks), big.NewInt(int64(NSPerSecond)))
	q := num.Quo(num, big.NewInt(int64(timescale)))
	if !q.IsInt64() {
		return 0, trace.E(trace.CodeInvalidArgument, trace.StageNormalize, "time conversion overflows int64: ticks=%d timescale=%d", ticks, timescale)
	}
	return NS(q.Int64()), nil
}

// Interval is the half-open interval [Start, End).
type Interval struct {
	Start NS `json:"start_ns"`
	End   NS `json:"end_ns"`
}

// Contains reports whether t belongs to the left-closed/right-open interval.
func (i Interval) Contains(t NS) bool {
	return i.Start <= t && t < i.End
}

// Length returns End-Start.
func (i Interval) Length() NS { return i.End - i.Start }

// Intersect returns the common half-open interval and whether it is non-empty.
func (i Interval) Intersect(other Interval) (Interval, bool) {
	start := maxNS(i.Start, other.Start)
	end := minNS(i.End, other.End)
	if start >= end {
		return Interval{}, false
	}
	return Interval{Start: start, End: end}, true
}

func minNS(a, b NS) NS {
	if a < b {
		return a
	}
	return b
}

func maxNS(a, b NS) NS {
	if a > b {
		return a
	}
	return b
}

// ByteRange is a half-open absolute file range.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// ContainsOffset reports whether off belongs to [Start, End).
func (r ByteRange) ContainsOffset(off int64) bool {
	return r.Start <= off && off < r.End
}

// Sample is one normalized media sample or fixture frame block.
type Sample struct {
	Index         int       `json:"index"`
	TrackID       int       `json:"track_id"`
	StartTick     uint64    `json:"start_tick"`
	DurationTick  uint32    `json:"duration_tick"`
	Start         NS        `json:"start_ns"`
	End           NS        `json:"end_ns"`
	BoxOffset     int64     `json:"box_offset"`
	PayloadOffset int64     `json:"payload_offset"`
	Range         ByteRange `json:"byte_range"`
	ExpectedCRC   uint32    `json:"expected_crc32,omitempty"`
	Data          []byte    `json:"-"`
	Complete      bool      `json:"complete"`
}

// Track is a normalized media track. OffsetNS maps track-local zero to the
// shared timeline.
type Track struct {
	ID           int       `json:"track_id"`
	Kind         string    `json:"kind"`
	Handler      string    `json:"handler"`
	TimeScale    uint32    `json:"timescale"`
	OffsetNS     NS        `json:"offset_ns"`
	IndexPresent bool      `json:"index_present"`
	Start        NS        `json:"start_ns"`
	End          NS        `json:"end_ns"`
	Samples      []*Sample `json:"-"`
}

// Interval returns the track's complete confirmed interval.
func (t *Track) Interval() Interval {
	return Interval{Start: t.Start, End: t.End}
}

// Damage describes an unconfirmable range and its exact reporting location.
type Damage struct {
	TrackID    int          `json:"track_id"`
	Range      Interval     `json:"range"`
	ByteOffset int64        `json:"byte_offset"`
	Code       trace.Code   `json:"code"`
	Stage      trace.Stage  `json:"stage"`
	Message    string       `json:"message"`
	Error      *trace.Error `json:"-"`
}

// Timeline is the normalized result for one source asset.
type Timeline struct {
	SourceDigest string   `json:"source_digest"`
	Tracks       []*Track `json:"tracks"`
	Damages      []Damage `json:"damages,omitempty"`
	Duration     NS       `json:"duration_ns"`
}

// Track returns the track with id.
func (tl *Timeline) Track(id int) *Track {
	for _, t := range tl.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Normalize validates tracks and fills all nanosecond endpoints. Empty tracks
// and timestamp regressions are located unified errors.
func Normalize(sourceDigest string, tracks []*Track, damages []Damage) (*Timeline, error) {
	tl := &Timeline{SourceDigest: sourceDigest, Tracks: tracks, Damages: damages}
	for _, tr := range tracks {
		if tr == nil {
			continue
		}
		if tr.TimeScale == 0 {
			return nil, trace.E(trace.CodeInvalidArgument, trace.StageNormalize, "track %d has zero timescale", tr.ID).WithTrack(tr.ID)
		}
		if len(tr.Samples) == 0 {
			return nil, trace.E(trace.CodeEmptyTrack, trace.StageNormalize, "track %d has no complete samples", tr.ID).WithTrack(tr.ID)
		}

		var trackStart, trackEnd NS
		for i, s := range tr.Samples {
			if s == nil {
				continue
			}
			if i > 0 && s.StartTick < tr.Samples[i-1].StartTick {
				ns, _ := TicksToNS(s.StartTick, tr.TimeScale)
				return nil, trace.E(
					trace.CodeTimestampRegression,
					trace.StageNormalize,
					"track %d timestamp regressed: tick %d after %d",
					tr.ID, s.StartTick, tr.Samples[i-1].StartTick,
				).WithTrack(tr.ID).WithTime(int64(tr.OffsetNS + ns)).WithOffset(s.BoxOffset)
			}

			localStart, err := TicksToNS(s.StartTick, tr.TimeScale)
			if err != nil {
				return nil, trace.Wrap(trace.StageNormalize, err).(*trace.Error).WithTrack(tr.ID)
			}
			endTick, overflow := addTicks(s.StartTick, uint64(s.DurationTick))
			if overflow {
				return nil, trace.E(trace.CodeInvalidArgument, trace.StageNormalize, "track %d endpoint tick overflow", tr.ID).WithTrack(tr.ID)
			}
			localEnd, err := TicksToNS(endTick, tr.TimeScale)
			if err != nil {
				return nil, trace.Wrap(trace.StageNormalize, err).(*trace.Error).WithTrack(tr.ID)
			}
			s.TrackID = tr.ID
			s.Start = tr.OffsetNS + localStart
			s.End = tr.OffsetNS + localEnd
			if i == 0 || s.Start < trackStart {
				trackStart = s.Start
			}
			if s.End > trackEnd {
				trackEnd = s.End
			}
		}
		tr.Start = trackStart
		tr.End = trackEnd
		if trackEnd > tl.Duration {
			tl.Duration = trackEnd
		}
	}
	return tl, nil
}

func addTicks(a, b uint64) (uint64, bool) {
	c := a + b
	if c < a {
		return 0, true
	}
	return c, false
}
