package clock

import (
	"tsrepair/internal/mediaerr"
)

// PTSMod is the modulus of the 33-bit PTS/DTS counter running at 90 kHz.
const PTSMod int64 = int64(1) << 33

// PTSHalfMod is the wrap decision threshold.
const PTSHalfMod = PTSMod / 2

// PTSUnwrapper expands 33-bit modulo PTS/DTS values onto a monotonic 90 kHz
// tick line.
type PTSUnwrapper struct {
	last    int64
	wrap    int64
	started bool
}

// Unwrap maps a raw 33-bit value onto the unwrapped line. A large backward
// jump is a wrap; a smaller one is a genuine discontinuity and is reported.
func (u *PTSUnwrapper) Unwrap(raw int64) (ticks int64, wrapped bool, err error) {
	if raw < 0 || raw >= PTSMod {
		return 0, false, mediaerr.Fatal(mediaerr.CodeTimeOverflow, 0, 0,
			"PTS value outside 33-bit range", "mark sample unindexable")
	}
	if !u.started {
		u.started = true
		u.last = raw
		return raw, false, nil
	}
	delta := raw - u.last
	if delta < -PTSHalfMod {
		next, aerr := CheckedAdd(u.wrap, PTSMod)
		if aerr != nil {
			return 0, false, aerr
		}
		u.wrap = next
		wrapped = true
	} else if delta < 0 {
		u.last = raw
		ticks, err = CheckedAdd(raw, u.wrap)
		return ticks, false, mediaerr.Recoverable(mediaerr.CodePTSBackward, 0, 0,
			"PTS moved backward without a wrap boundary",
			"mark affected sample unindexable")
	}
	u.last = raw
	ticks, err = CheckedAdd(raw, u.wrap)
	return ticks, wrapped, err
}

// Started reports whether any value has been expanded.
func (u *PTSUnwrapper) Started() bool { return u.started }

// DuplicatePolicy controls how repeated PTS values are handled.
type DuplicatePolicy int

const (
	// DuplicateDrop marks repeated samples unindexable and keeps them out of
	// the random-access index.
	DuplicateDrop DuplicatePolicy = iota
	// DuplicateKeep retains repeated samples but flags them.
	DuplicateKeep
)

// Sample is one decoded presentation unit on the repaired timeline.
type Sample struct {
	PTS90k     int64 // unwrapped 90 kHz presentation ticks
	DTS90k     int64 // unwrapped 90 kHz decode ticks
	WallNanos  int64 // PCR-anchored wall time, 0 when no anchor exists
	IsKeyframe bool
	Indexable  bool
	Duplicate  bool
	ByteOffset int64
}

// TimelineConfig parameterises sample timeline repair.
type TimelineConfig struct {
	// Duplicates selects the handling of repeated PTS values.
	Duplicates DuplicatePolicy
}

// Timeline repairs PTS/DTS for one elementary stream. Variable frame rate is
// the native model: intervals are taken from actual PTS deltas, never forced
// onto a fixed grid.
type Timeline struct {
	cfg  TimelineConfig
	ptsU PTSUnwrapper
	dtsU PTSUnwrapper
	ec   *mediaerr.Collector
	pid  uint16

	anchorPTS   int64
	anchorNanos int64
	anchored    bool

	prevPTS int64
	samples []Sample
}

// NewTimeline builds an empty timeline for PID pid.
func NewTimeline(pid uint16, cfg TimelineConfig, ec *mediaerr.Collector) *Timeline {
	return &Timeline{cfg: cfg, ec: ec, pid: pid}
}

// SetPCRAnchor binds the unwrapped 90 kHz PTS line to a PCR-derived wall
// clock. pcrTicks27 is an unwrapped 27 MHz PCR value; the anchor takes effect
// for the given unwrapped PTS tick.
func (t *Timeline) SetPCRAnchor(ptsTicks90k, pcrTicks27 int64) error {
	nanos, err := TicksToNanos(pcrTicks27, PCRRate)
	if err != nil {
		return err
	}
	t.anchorPTS = ptsTicks90k
	t.anchorNanos = nanos
	t.anchored = true
	return nil
}

// Anchored reports whether a PCR anchor exists.
func (t *Timeline) Anchored() bool { return t.anchored }

// AddSample repairs one sample. Raw PTS/DTS are 33-bit modulo values. The
// returned sample is always non-nil; unindexable samples carry Indexable=false
// and a structured error has been recorded.
func (t *Timeline) AddSample(rawPTS, rawDTS int64, keyframe bool, offset int64) (*Sample, error) {
	pts, wrapPTS, err := t.ptsU.Unwrap(rawPTS)
	if err != nil {
		t.record(err, offset)
		return t.unindexable(rawPTS, rawDTS, keyframe, offset), nil
	}
	if wrapPTS {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodePTSWrap, t.pid, offset,
			"PTS crossed the 33-bit wrap boundary", "expand with accumulated wrap count"))
	}
	dts, _, err := t.dtsU.Unwrap(rawDTS)
	if err != nil {
		t.record(err, offset)
		return t.unindexable(pts, rawDTS, keyframe, offset), nil
	}
	s := &Sample{PTS90k: pts, DTS90k: dts, IsKeyframe: keyframe, Indexable: true, ByteOffset: offset}

	if dts > pts {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodePTSBackward, t.pid, offset,
			"DTS is later than PTS", "mark sample unindexable"))
		s.Indexable = false
	}
	if len(t.samples) > 0 && pts == t.prevPTS {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodePTSDuplicate, t.pid, offset,
			"duplicate PTS value", "apply configured duplicate policy"))
		s.Duplicate = true
		if t.cfg.Duplicates == DuplicateDrop {
			s.Indexable = false
		}
	}

	if !t.anchored {
		t.ec.Add(mediaerr.Recoverable(mediaerr.CodePTSNoAnchor, t.pid, offset,
			"sample arrived before any PCR anchor",
			"retain ticks; mark unindexable until an anchor is known"))
		s.Indexable = false
	} else {
		delta90k, derr := CheckedAdd(pts, -t.anchorPTS)
		if derr != nil {
			t.record(derr, offset)
			s.Indexable = false
		} else {
			deltaNanos, cerr := MulScale(delta90k, 1_000_000_000, PTSRate)
			if cerr != nil {
				t.record(cerr, offset)
				s.Indexable = false
			} else {
				wall, aerr := CheckedAdd(t.anchorNanos, deltaNanos)
				if aerr != nil {
					t.record(aerr, offset)
					s.Indexable = false
				} else {
					s.WallNanos = wall
				}
			}
		}
	}

	t.prevPTS = pts
	t.samples = append(t.samples, *s)
	return s, nil
}

func (t *Timeline) record(err error, offset int64) {
	if me, ok := err.(*mediaerr.Error); ok {
		me.PID = t.pid
		me.Offset = offset
		t.ec.Add(me)
	}
}

func (t *Timeline) unindexable(pts, dts int64, keyframe bool, offset int64) *Sample {
	s := &Sample{PTS90k: pts, DTS90k: dts, IsKeyframe: keyframe, Indexable: false, ByteOffset: offset}
	t.samples = append(t.samples, *s)
	return s
}

// Samples returns the repaired samples in arrival order.
func (t *Timeline) Samples() []Sample { return t.samples }

// IntervalNanos returns the actual PTS delta between indexable sample pair i
// and i+1 in nanoseconds, preserving variable frame rate spacing.
func (t *Timeline) IntervalNanos(i int) (int64, bool) {
	var walls []int64
	for k := range t.samples {
		if t.samples[k].Indexable {
			walls = append(walls, t.samples[k].WallNanos)
		}
	}
	if i < 0 || i+1 >= len(walls) {
		return 0, false
	}
	d, err := AbsDiff(walls[i+1], walls[i])
	return d, err == nil
}

// Interval90k returns the exact PTS delta between indexable sample pair i
// and i+1 in 90 kHz ticks. VFR decisions use this exact representation rather
// than nanosecond values truncated by integer division.
func (t *Timeline) Interval90k(i int) (int64, bool) {
	var pts []int64
	for k := range t.samples {
		if t.samples[k].Indexable {
			pts = append(pts, t.samples[k].PTS90k)
		}
	}
	if i < 0 || i+1 >= len(pts) {
		return 0, false
	}
	d, err := AbsDiff(pts[i+1], pts[i])
	return d, err == nil
}

// IsVFR reports whether indexable samples use non-uniform spacing: more than
// one distinct presentation interval is present. Comparison is done on exact
// 90 kHz tick deltas so nanosecond truncation cannot cause false positives.
func (t *Timeline) IsVFR() bool {
	seen := map[int64]struct{}{}
	for i := 0; ; i++ {
		d, ok := t.Interval90k(i)
		if !ok {
			break
		}
		seen[d] = struct{}{}
		if len(seen) > 1 {
			return true
		}
	}
	return false
}
