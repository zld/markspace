// Package clock defines the shared nanosecond time base, per-track clock
// parameters and the error taxonomy reused by the demuxer and the RTP
// recovery layer. Timestamps from every capture device are reduced to the
// same int64 nanosecond scale before they reach the merge stage.
package clock

import (
	"errors"
	"fmt"
	"sync"
)

// Kind classifies clock-domain failures so callers never match on strings.
type Kind int

const (
	KindOK           Kind = iota
	KindMonotonicity      // a sample moved backwards relative to the prior sample
	KindWrap              // a wrap could not be resolved consistently
	KindScale             // an unusable timecode scale was supplied
	KindDrift             // the estimated device drift changed abruptly
	KindPacketLoss        // an RTP gap stayed open past the recovery window
)

// Error is the common failure type for the demuxer, clock and RTP recovery.
type Error struct {
	Kind  Kind
	Track uint32 // Matroska track number / RTP ssc-derived track id
	Seq   uint64 // block or RTP sequence number, zero when unavailable
	Msg   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("clock: %s (track=%d seq=%d)", e.Msg, e.Track, e.Seq)
}

// Is supports errors.Is on Kind across every package that embeds the type.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind
}

func fail(kind Kind, track uint32, seq uint64, format string, args ...any) error {
	return &Error{Kind: kind, Track: track, Seq: seq, Msg: fmt.Sprintf(format, args...)}
}

// TrackClock describes one device clock. TicksPerSecond maps raw timecode
// ticks to seconds; WrapAt (ticks, >0) enables modulo unwrapping; OriginTicks
// is the non-zero first timecode that should map to t=0; DriftPPM is the
// signed static rate error of the device oscillator.
type TrackClock struct {
	Track          uint32
	TicksPerSecond int64
	WrapAt         int64
	OriginTicks    int64
	DriftPPM       float64
}

// trackState holds the reduction state of one track.
type trackState struct {
	cfg      TrackClock
	originNS int64 // nanosecond origin already reduced to the shared base
	lastRaw  int64 // last raw (pre-unwrap) tick, for wrap resolution
	lastAbs  int64 // last unwrapped tick
	hasLast  bool
	lastNS   int64 // last corrected nanosecond timestamp
	drift    *DriftModel
}

// Timeline reduces per-track ticks onto one monotonic nanosecond base.
// A Timeline is safe for concurrent use across tracks; per-track ordering is
// still the caller's responsibility (samples of one track must arrive in
// sequence-number order).
type Timeline struct {
	mu     sync.Mutex
	tracks map[uint32]*trackState
}

// NewTimeline creates an empty timeline.
func NewTimeline() *Timeline {
	return &Timeline{tracks: make(map[uint32]*trackState)}
}

// Add registers a track clock. It fails on duplicate registration or an
// unusable scale, before any sample is reduced.
func (t *Timeline) Add(cfg TrackClock) error {
	if cfg.TicksPerSecond <= 0 {
		return fail(KindScale, cfg.Track, 0, "ticks-per-second must be positive, got %d", cfg.TicksPerSecond)
	}
	if cfg.WrapAt < 0 {
		return fail(KindWrap, cfg.Track, 0, "wrap point must be positive or zero")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.tracks[cfg.Track]; exists {
		return fail(KindScale, cfg.Track, 0, "duplicate track registration")
	}
	t.tracks[cfg.Track] = &trackState{
		cfg:   cfg,
		drift: NewDriftModel(cfg.DriftPPM),
	}
	return nil
}

// Has reports whether a track is registered.
func (t *Timeline) Has(track uint32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.tracks[track]
	return ok
}

// unwrap resolves a raw tick against the previous sample. With no wrap point
// configured the raw value is already absolute.
func (s *trackState) unwrap(raw int64) (int64, error) {
	if s.cfg.WrapAt <= 0 {
		return raw, nil
	}
	w := s.cfg.WrapAt
	if raw < 0 || raw >= w {
		return 0, fail(KindWrap, s.cfg.Track, 0,
			"tick %d outside wrap modulus %d", raw, w)
	}
	if !s.hasLast {
		return raw, nil
	}
	abs := s.lastAbs
	switch {
	case raw >= s.lastRaw:
		abs += raw - s.lastRaw
	default:
		// Forward wrap: raw jumped low. A jump larger than half the modulus
		// is ambiguous and treated as a wrap failure rather than guessed.
		delta := s.lastRaw - raw
		if delta > w/2 {
			abs += w - delta
		} else {
			return 0, fail(KindWrap, s.cfg.Track, 0,
				"ambiguous wrap: raw %d after %d within modulus %d", raw, s.lastRaw, w)
		}
	}
	return abs, nil
}

// ticksToNS converts tick deltas to nanoseconds with integer rounding.
func ticksToNS(delta, ticksPerSecond int64) int64 {
	// Split to avoid overflow on long streams.
	sec := delta / ticksPerSecond
	rem := delta % ticksPerSecond
	return sec*1_000_000_000 + (rem*1_000_000_000)/ticksPerSecond
}

// Reduce maps one raw sample to shared-base nanoseconds. seq is carried into
// error diagnostics. Monotonicity violations are returned as KindMonotonicity
// and never silently repaired: state is left untouched so the failure is
// observable on every retry.
func (t *Timeline) Reduce(track uint32, seq uint64, raw int64) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.tracks[track]
	if !ok {
		return 0, fail(KindScale, track, seq, "unregistered track")
	}
	abs, err := s.unwrap(raw)
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			ce.Seq = seq
		}
		return 0, err
	}
	rel := abs - s.cfg.OriginTicks
	if rel < 0 {
		return 0, fail(KindWrap, track, seq,
			"sample at tick %d predates origin %d", abs, s.cfg.OriginTicks)
	}
	ns := ticksToNS(rel, s.cfg.TicksPerSecond)
	ns = s.drift.Correct(ns)
	if s.hasLast && ns <= s.lastNS {
		return 0, fail(KindMonotonicity, track, seq,
			"timestamp %d ns does not advance past %d ns", ns, s.lastNS)
	}
	s.lastRaw = raw
	s.lastAbs = abs
	s.lastNS = ns
	s.hasLast = true
	return ns, nil
}

// LastNS returns the most recently reduced timestamp for a track, or false
// when no sample has been reduced yet.
func (t *Timeline) LastNS(track uint32) (int64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.tracks[track]
	if !ok || !s.hasLast {
		return 0, false
	}
	return s.lastNS, true
}
