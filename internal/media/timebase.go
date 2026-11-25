package media

import (
	"fmt"
	"math/bits"
	"sort"
)

// Timebase describes one track's native clock. Media times are stored and
// compared in nanoseconds, but every track keeps its original timescale so
// conversions stay integer-exact instead of drifting through float64.
type Timebase struct {
	// Track is the logical track name ("audio", "video", "subtitle", ...).
	Track string
	// Timescale is ticks per second (mdhd timescale or RTP clock rate).
	Timescale uint32
	// OriginTicks is the presentation offset of tick 0 on the shared
	// nanosecond timeline, expressed in this track's own ticks. MP4 tracks
	// derive it from an edit-list delay; RTP tracks use the timestamp of the
	// first committed packet.
	OriginTicks uint64
}

// Validate reports whether the timebase can participate in conversions.
func (tb Timebase) Validate() error {
	if tb.Track == "" {
		return New(KindTimebase, "empty track name")
	}
	if tb.Timescale == 0 {
		return At(Newf(KindTimebase, "zero timescale for track %q", tb.Track),
			Loc{Track: tb.Track})
	}
	return nil
}

// String renders the timebase deterministically for reports and digests.
func (tb Timebase) String() string {
	return fmt.Sprintf("%s/%d+%d", tb.Track, tb.Timescale, tb.OriginTicks)
}

// ToNs converts ticks in tb to nanoseconds on the shared timeline. The
// product is computed at 128-bit width, so the only failure modes are
// overflow and a zero timescale.
func (tb Timebase) ToNs(ticks uint64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	if ticks > ^uint64(0)-tb.OriginTicks {
		return 0, At(Newf(KindTimebase, "tick overflow: ticks=%d origin=%d", ticks, tb.OriginTicks),
			Loc{Track: tb.Track})
	}
	abs := ticks + tb.OriginTicks
	hi, lo := bits.Mul64(abs, 1_000_000_000)
	if hi >= uint64(tb.Timescale) {
		return 0, At(Newf(KindTimebase, "nanosecond overflow: ticks=%d timescale=%d", abs, tb.Timescale),
			Loc{Track: tb.Track})
	}
	q, _ := bits.Div64(hi, lo, uint64(tb.Timescale))
	if q > 1<<63-1 {
		return 0, At(Newf(KindTimebase, "nanosecond overflow: ticks=%d timescale=%d", abs, tb.Timescale),
			Loc{Track: tb.Track})
	}
	return int64(q), nil
}

// FromNs converts a non-negative nanosecond time back to ticks in tb,
// truncating toward zero. Times before the track origin are rejected rather
// than silently clamped.
func (tb Timebase) FromNs(ns int64) (uint64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	if ns < 0 {
		return 0, At(Newf(KindTimebase, "negative media time: %dns", ns), Loc{Track: tb.Track})
	}
	hi, lo := bits.Mul64(uint64(ns), uint64(tb.Timescale))
	q, _ := bits.Div64(hi, lo, 1_000_000_000)
	if q < tb.OriginTicks {
		return 0, At(Newf(KindTimebase, "media time precedes track origin: %dns origin=%d ticks",
			ns, tb.OriginTicks), Loc{Track: tb.Track})
	}
	return q - tb.OriginTicks, nil
}

// Ratio is an exact reduced rational factor between two tick clocks.
type Ratio struct {
	Num, Den uint64
}

// NewRatio reduces num/den. A zero denominator is a structured error.
func NewRatio(num, den uint64) (Ratio, error) {
	if den == 0 {
		return Ratio{}, New(KindTimebase, "zero ratio denominator")
	}
	return Ratio{Num: num / gcd(num, den), Den: den / gcd(num, den)}, nil
}

// Scaled converts a tick count from src to dst without going through
// nanoseconds. Results that would lose more than one tick are rejected so a
// caller cannot quietly round a timebase it does not understand.
func Scaled(ticks uint64, src, dst Timebase) (uint64, error) {
	if err := src.Validate(); err != nil {
		return 0, err
	}
	if err := dst.Validate(); err != nil {
		return 0, err
	}
	num, den := uint64(dst.Timescale), uint64(src.Timescale)
	g := gcd(num, den)
	num, den = num/g, den/g
	hi, lo := bits.Mul64(ticks, num)
	if hi >= den {
		return 0, At(Newf(KindTimebase,
			"unscalable timebase: track %q -> %q ticks=%d overflow", src.Track, dst.Track, ticks),
			Loc{Track: src.Track})
	}
	q, rem := bits.Div64(hi, lo, den)
	if rem != 0 {
		return 0, At(Newf(KindTimebase,
			"unscalable timebase: track %q -> %q ticks=%d produces a fractional tick",
			src.Track, dst.Track, ticks), Loc{Track: src.Track})
	}
	return q, nil
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// TrackList is a small validated set of timebases keyed by track name.
type TrackList struct {
	tracks map[string]Timebase
}

// NewTrackList returns an empty list.
func NewTrackList() *TrackList {
	return &TrackList{tracks: make(map[string]Timebase, 4)}
}

// Put validates and stores tb.
func (tl *TrackList) Put(tb Timebase) error {
	if err := tb.Validate(); err != nil {
		return err
	}
	tl.tracks[tb.Track] = tb
	return nil
}

// Get returns the timebase registered for a track.
func (tl *TrackList) Get(track string) (Timebase, bool) {
	tb, ok := tl.tracks[track]
	return tb, ok
}

// All returns the registered tracks sorted by name.
func (tl *TrackList) All() []Timebase {
	out := make([]Timebase, 0, len(tl.tracks))
	for _, tb := range tl.tracks {
		out = append(out, tb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Track < out[j].Track })
	return out
}
