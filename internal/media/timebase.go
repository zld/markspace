package media

import (
	"fmt"
	"math/big"
	"sort"
)

// Timebase describes one track's native clock. Media times are stored and
// compared in nanoseconds, but every track keeps its original timescale so
// conversions use exact rational arithmetic instead of drifting through
// float64.
//
// The mapping is affine: a tick count t maps to
//
//	OriginNs + (t - OriginTicks) * 1e9 / Timescale
//
// Both origins are signed because a track may start after another on the
// shared timeline (positive offset) and because RTP timestamps wrap past a
// 32-bit boundary.
type Timebase struct {
	// Track is the logical track name ("audio", "video", "subtitle", ...).
	Track string
	// Timescale is ticks per second (mdhd timescale or RTP clock rate).
	Timescale uint32
	// OriginTicks is the tick value that maps to OriginNs.
	OriginTicks int64
	// OriginNs is the shared-timeline nanosecond time of OriginTicks.
	OriginNs int64
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
	return fmt.Sprintf("%s/%d@%d=%dns", tb.Track, tb.Timescale, tb.OriginTicks, tb.OriginNs)
}

// nsFor computes (ticks - OriginTicks) * 1e9 / Timescale with arbitrary
// precision, requires the result to fit in an int64, and adds OriginNs with an
// overflow check. Big integers keep every intermediate exact; only the final
// truncation toward zero is lossy.
func (tb Timebase) nsFor(ticks int64) (int64, error) {
	delta := new(big.Int).Sub(big.NewInt(ticks), big.NewInt(tb.OriginTicks))
	delta.Mul(delta, big.NewInt(1_000_000_000))
	delta.Quo(delta, big.NewInt(int64(tb.Timescale)))
	if !delta.IsInt64() {
		return 0, At(Newf(KindTimebase,
			"timebase overflow: ticks=%d timescale=%d originTicks=%d", ticks, tb.Timescale, tb.OriginTicks),
			Loc{Track: tb.Track})
	}
	ns, overflow := addInt64(delta.Int64(), tb.OriginNs)
	if overflow {
		return 0, At(Newf(KindTimebase, "timebase overflow adding origin %dns", tb.OriginNs),
			Loc{Track: tb.Track})
	}
	return ns, nil
}

func addInt64(a, b int64) (int64, bool) {
	if b > 0 && a > 1<<63-1-b {
		return 0, true
	}
	if b < 0 && a < -1<<63-b {
		return 0, true
	}
	return a + b, false
}

// ToNs converts ticks in tb to nanoseconds on the shared timeline. Results
// before the shared zero are allowed and simply negative; callers that need
// non-negative media time reject them.
func (tb Timebase) ToNs(ticks uint64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	if ticks > 1<<63-1 {
		return 0, At(Newf(KindTimebase, "tick count too large: %d", ticks), Loc{Track: tb.Track})
	}
	return tb.nsFor(int64(ticks))
}

// FromNs converts a nanosecond time back to ticks in tb, truncating
// sub-tick remainders toward zero. Negative results are preserved because a
// track may legitimately be mapped to times before its first tick.
func (tb Timebase) FromNs(ns int64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	rel := new(big.Int).Sub(big.NewInt(ns), big.NewInt(tb.OriginNs))
	rel.Mul(rel, big.NewInt(int64(tb.Timescale)))
	rel.Quo(rel, big.NewInt(1_000_000_000))
	if !rel.IsInt64() {
		return 0, At(Newf(KindTimebase, "timebase overflow: %dns timescale=%d", ns, tb.Timescale),
			Loc{Track: tb.Track})
	}
	ticks := new(big.Int).Add(rel, big.NewInt(tb.OriginTicks))
	if !ticks.IsInt64() {
		return 0, At(Newf(KindTimebase, "tick overflow: %dns timescale=%d", ns, tb.Timescale),
			Loc{Track: tb.Track})
	}
	out := ticks.Int64()
	// Negative ticks are meaningful when OriginNs is positive: they name
	// times before the track's first tick on the shared timeline. Callers
	// that need a non-negative tick (e.g. a container edit) check for it.
	return out, nil
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
	g := gcd(num, den)
	return Ratio{Num: num / g, Den: den / g}, nil
}

// Scaled converts a tick count from src's clock into dst's clock without
// going through nanoseconds. Conversion is exact-or-error: a result that
// needs a fractional tick is rejected rather than rounded.
func Scaled(ticks uint64, src, dst Timebase) (uint64, error) {
	if err := src.Validate(); err != nil {
		return 0, err
	}
	if err := dst.Validate(); err != nil {
		return 0, err
	}
	p := new(big.Int).SetUint64(ticks)
	p.Mul(p, big.NewInt(int64(dst.Timescale)))
	q, rem := new(big.Int).QuoRem(p, big.NewInt(int64(src.Timescale)), new(big.Int))
	if rem.Sign() != 0 {
		return 0, At(Newf(KindTimebase,
			"unscalable timebase: track %q -> %q ticks=%d produces a fractional tick",
			src.Track, dst.Track, ticks), Loc{Track: src.Track})
	}
	if !q.IsUint64() {
		return 0, At(Newf(KindTimebase,
			"unscalable timebase: track %q -> %q ticks=%d overflow",
			src.Track, dst.Track, ticks), Loc{Track: src.Track})
	}
	return q.Uint64(), nil
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
