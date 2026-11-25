// Package clock converts every track's native timestamp into one integer
// nanosecond timeline. Conversion is a pure function of the raw ticks plus the
// declared timescale, origin and 32-bit anchor, so replaying an event that
// arrives late produces exactly the same nanosecond value as processing it in
// order.
package clock

import (
	"fmt"
	"math"

	"archive.example/mediakernel/internal/events"
)

const (
	// NSPerSecond is the unified timebase: integer nanoseconds.
	NSPerSecond int64 = 1_000_000_000
	// Width32 marks 32-bit native timestamps (RTP).
	Width32 = 32
	// Width64 marks 64-bit native timestamps (MP4/emsg/SEI).
	Width64 = 64
	// MaxTimelineNS bounds a single archive: 24 hours.
	MaxTimelineNS int64 = 24 * 60 * 60 * NSPerSecond
	// TickLimit is the modulus of a 32-bit timestamp.
	TickLimit32 int64 = 1 << 32
)

// MapTicks converts a native tick count to nanoseconds on the unified
// timeline. It rounds half away from zero on the sub-nanosecond remainder and
// returns a typed archive error for every rejected input.
func MapTicks(track events.TrackID, ticks int64, timescale uint32, originNS, maxNS int64, seq uint64, offset int64) (int64, *events.Error) {
	if timescale == 0 {
		return 0, events.NewError(events.CodeInvalidTimescale, track, seq, offset, "map", "timescale is zero", nil)
	}
	if maxNS <= 0 {
		maxNS = MaxTimelineNS
	}
	scale := int64(timescale)

	// Split before multiplying so that hit * NSPerSecond cannot overflow.
	hi := ticks / scale
	lo := ticks % scale
	if lo < 0 {
		lo += scale
		hi--
	}
	// Bound the whole-second part against the configured timeline limit.
	if hi > (maxNS+NSPerSecond)/NSPerSecond || hi < -(maxNS+NSPerSecond)/NSPerSecond {
		return 0, events.NewError(events.CodeTimeOutOfRange, track, seq, offset, "map",
			fmt.Sprintf("ticks=%d timescale=%d exceeds timeline limit %dns", ticks, timescale, maxNS), nil)
	}
	frac := (lo*NSPerSecond + scale/2) / scale
	ns := hi*NSPerSecond + frac + originNS
	if ns < -maxNS || ns > maxNS {
		return 0, events.NewError(events.CodeTimeOutOfRange, track, seq, offset, "map",
			fmt.Sprintf("timestamp %dns outside [-%d,%d]", ns, maxNS, maxNS), nil)
	}
	return ns, nil
}

// Unwrap32 resolves a wrapping 32-bit timestamp to the extended tick count
// nearest the anchor. The result is a pure function of (anchor, raw): arrival
// order cannot influence it, which is what makes late replay deterministic.
// The ambiguity only exists at exactly half the modulus; that tie resolves
// towards the earlier (negative) epoch.
func Unwrap32(anchor, raw uint32) int64 {
	a := int64(anchor)
	r := int64(raw)
	best := r
	bestDist := abs64(r - a)
	for _, e := range []int64{-TickLimit32, TickLimit32} {
		cand := r + e
		if d := abs64(cand - a); d < bestDist {
			best, bestDist = cand, d
		}
	}
	return best
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// TicksFromNS is the inverse of MapTicks for a given track scale, used when
// rendering needs to align frame boundaries to native track ticks.
func TicksFromNS(track events.TrackID, ns int64, timescale uint32, originNS int64, seq uint64, offset int64) (int64, *events.Error) {
	if timescale == 0 {
		return 0, events.NewError(events.CodeInvalidTimescale, track, seq, offset, "inverse", "timescale is zero", nil)
	}
	rel := ns - originNS
	scale := int64(timescale)
	hi := rel / NSPerSecond
	lo := rel % NSPerSecond
	if lo < 0 {
		lo += NSPerSecond
		hi--
	}
	t := hi*scale + (lo*scale+NSPerSecond/2)/NSPerSecond
	return t, nil
}

// Converter is a per-track configuration binding native ticks to the unified
// nanosecond timeline.
type Converter struct {
	track     events.TrackID
	timescale uint32
	originNS  int64
	maxNS     int64
	width     int

	anchorRaw uint32
	hasAnchor bool
}

// Options configures a Converter.
type Options struct {
	Track     events.TrackID
	Timescale uint32
	OriginNS  int64
	MaxNS     int64 // defaults to MaxTimelineNS
	Width     int   // Width32 or Width64
	Anchor    uint32
	HasAnchor bool
}

// NewConverter validates and constructs a converter. An invalid timescale is
// a fatal archive error.
func NewConverter(opt Options) (*Converter, *events.Error) {
	if opt.Timescale == 0 {
		return nil, events.NewError(events.CodeInvalidTimescale, opt.Track, 0, -1, "converter", "timescale is zero", nil)
	}
	if opt.Width != Width32 && opt.Width != Width64 {
		return nil, events.NewError(events.CodeInternal, opt.Track, 0, -1, "converter",
			fmt.Sprintf("unsupported timestamp width %d", opt.Width), nil)
	}
	if opt.MaxNS <= 0 {
		opt.MaxNS = MaxTimelineNS
	}
	if abs64(opt.OriginNS) > opt.MaxNS {
		return nil, events.NewError(events.CodeTimeOutOfRange, opt.Track, 0, -1, "converter",
			fmt.Sprintf("origin %dns exceeds timeline limit %d", opt.OriginNS, opt.MaxNS), nil)
	}
	return &Converter{
		track:     opt.Track,
		timescale: opt.Timescale,
		originNS:  opt.OriginNS,
		maxNS:     opt.MaxNS,
		width:     opt.Width,
		anchorRaw: opt.Anchor,
		hasAnchor: opt.HasAnchor,
	}, nil
}

// Track returns the bound track.
func (c *Converter) Track() events.TrackID { return c.track }

// Timescale returns the native ticks per second.
func (c *Converter) Timescale() uint32 { return c.timescale }

// OriginNS returns the nanosecond value of native tick zero.
func (c *Converter) OriginNS() int64 { return c.originNS }

// Width returns the native timestamp width.
func (c *Converter) Width() int { return c.width }

// ToNS converts raw native ticks to the unified nanosecond timeline.
func (c *Converter) ToNS(raw uint64, seq uint64, offset int64) (int64, *events.Error) {
	ticks := int64(raw)
	if c.width == Width32 {
		u := uint32(raw)
		if !c.hasAnchor {
			// Without an explicit anchor the stream baseline is tick zero;
			// raw values then behave as unsigned distances from the start.
			return MapTicks(c.track, int64(u), c.timescale, c.originNS, c.maxNS, seq, offset)
		}
		ticks = Unwrap32(c.anchorRaw, u)
	}
	return MapTicks(c.track, ticks, c.timescale, c.originNS, c.maxNS, seq, offset)
}

// ToNS32 is ToNS for a 32-bit raw value.
func (c *Converter) ToNS32(raw uint32, seq uint64, offset int64) (int64, *events.Error) {
	return c.ToNS(uint64(raw), seq, offset)
}

// FromNS is the inverse mapping, used for native-tick alignment checks.
func (c *Converter) FromNS(ns int64, seq uint64, offset int64) (int64, *events.Error) {
	return TicksFromNS(c.track, ns, c.timescale, c.originNS, seq, offset)
}

// SetAnchor installs the 32-bit anchor used to unwrap subsequent timestamps.
// Callers should derive the anchor from the canonical (lowest) input sequence
// number, not from arrival order.
func (c *Converter) SetAnchor(raw uint32) {
	c.anchorRaw = raw
	c.hasAnchor = true
}

// DurationNS converts a native duration to nanoseconds without origin offset
// and without applying the archive wall-clock range: a duration may be far
// longer than the 24h timeline bound (for example a multi-minute chapter).
// It keeps the split-before-multiply overflow safety of MapTicks and rejects
// only a duration that cannot be represented in int64 nanoseconds.
func DurationNS(ticks int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, ErrInvalidTimescale
	}
	if ticks == math.MinInt64 {
		return 0, events.NewError(events.CodeTimeOutOfRange, "", 0, -1, "duration",
			"duration magnitude exceeds int64", nil)
	}
	scale := int64(timescale)
	neg := ticks < 0
	t := ticks
	if neg {
		t = -t
	}
	hi := t / scale
	lo := t % scale
	// lo < scale <= math.MaxUint32, so lo*NSPerSecond cannot overflow int64.
	frac := (lo*NSPerSecond + scale/2) / scale
	// Guard hi*NSPerSecond+frac against int64 overflow.
	if hi > (math.MaxInt64-frac)/NSPerSecond {
		return 0, events.NewError(events.CodeTimeOutOfRange, "", 0, -1, "duration",
			fmt.Sprintf("duration ticks=%d timescale=%d overflows int64 nanoseconds", ticks, timescale), nil)
	}
	ns := hi*NSPerSecond + frac
	if neg {
		ns = -ns
	}
	return ns, nil
}

// ErrInvalidTimescale is returned by helpers that cannot report a typed error.
var ErrInvalidTimescale = fmt.Errorf("invalid timescale: zero")

func init() {
	// Guard against accidental narrowing of the limit constant.
	if MaxTimelineNS <= 0 {
		panic("clock: MaxTimelineNS must be positive")
	}
}
