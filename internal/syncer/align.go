// Package syncer aligns audio and video streams on the shared media clock.
//
// Each track carries a base offset that maps its local extended timeline onto
// the common clock. That base offset comes from the initial alignment (for
// example an RTCP pairing of the streams) and is applied unconditionally.
//
// Re-alignment is a separate, bounded operation. As drift is estimated the
// aligner may be asked to shift a track by some number of ticks; if that
// correction exceeds the configured maximum it is refused and the track is
// marked out of sync. While out of sync, samples keep their original
// timestamps and are flagged, so nothing is silently fabricated and the
// affected segments are identifiable downstream.
package syncer

import (
	"fmt"

	"example.com/mediaforge/internal/merrors"
)

// Kind distinguishes the media types being aligned.
type Kind string

const (
	KindAudio Kind = "audio"
	KindVideo Kind = "video"
)

// Track describes one input stream's timing relationship to the shared clock.
type Track struct {
	Kind Kind
	SSRC uint32
	// ClockRate is the RTP clock rate in ticks per second.
	ClockRate uint32
	// OffsetTicks maps this track onto the common clock, in reference-rate
	// ticks: Common = Extended*ReferenceRate/ClockRate + OffsetTicks. It is
	// established by initial alignment and is not subject to the correction
	// budget.
	OffsetTicks int64
}

// Sample is one media unit positioned on its track's timeline.
type Sample struct {
	Track    Kind
	SSRC     uint32
	Extended uint64 // track-local extended timestamp, ticks
	Duration uint64 // sample duration, ticks; zero when unknown
	Keyframe bool
	Payload  []byte
}

// Aligned is the same sample mapped onto the common clock.
type Aligned struct {
	Sample
	// CommonTick is the sample's position on the shared clock.
	CommonTick uint64
	// Correction is the signed shift most recently applied to this track's
	// alignment, in ticks.
	Correction int64
	// InSync is false when the track is out of sync, either because a
	// correction was refused or because the sample moved backwards past the
	// correction budget. Out-of-sync samples keep their track-local position
	// as CommonTick.
	InSync bool
}

// Config bounds the alignment behaviour.
type Config struct {
	// MaxCorrectionTicks is the largest correction the aligner will apply or
	// tolerate. Larger corrections are refused and mark the track out of sync.
	MaxCorrectionTicks uint64
	// ReferenceRate is the shared clock rate used for diagnostics, ticks/sec.
	ReferenceRate uint32
}

// DefaultConfig returns a 90 kHz reference with a 100 ms correction budget.
func DefaultConfig() Config {
	return Config{MaxCorrectionTicks: 9000, ReferenceRate: 90000}
}

func (c Config) validate() error {
	if c.MaxCorrectionTicks == 0 {
		return merrors.New(merrors.StageSync, merrors.Invalid, "max correction must be positive")
	}
	if c.ReferenceRate == 0 || c.ReferenceRate > 1_000_000 {
		return merrors.New(merrors.StageSync, merrors.Invalid,
			"reference rate %d outside 1..1000000", c.ReferenceRate)
	}
	return nil
}

// Stats counts alignment outcomes.
type Stats struct {
	Aligned       int
	OutOfSync     int
	Refused       int
	MaxCorrection uint64
}

type trackState struct {
	track      Track
	adjust     int64 // accumulated correction applied on top of the base offset
	outOfSync  bool
	haveLast   bool
	lastCommon uint64
}

// Aligner maps track samples onto a shared clock.
type Aligner struct {
	cfg    Config
	tracks map[Kind]*trackState
	st     Stats
}

// New creates an aligner.
func New(cfg Config) (*Aligner, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Aligner{cfg: cfg, tracks: make(map[Kind]*trackState)}, nil
}

// AddTrack registers a track. Re-registering a kind replaces the track and
// clears its accumulated correction.
func (a *Aligner) AddTrack(t Track) error {
	if t.Kind != KindAudio && t.Kind != KindVideo {
		return merrors.New(merrors.StageSync, merrors.Invalid, "unknown track kind %q", t.Kind)
	}
	if t.ClockRate == 0 {
		return merrors.New(merrors.StageSync, merrors.Invalid, "track clock rate must be positive")
	}
	a.tracks[t.Kind] = &trackState{track: t}
	return nil
}

// Track returns a registered track's current definition, including any
// accumulated correction folded into OffsetTicks.
func (a *Aligner) Track(k Kind) (Track, bool) {
	ts, ok := a.tracks[k]
	if !ok {
		return Track{}, false
	}
	t := ts.track
	t.OffsetTicks += ts.adjust
	return t, true
}

// InSync reports whether a track is currently aligned within budget.
func (a *Aligner) InSync(k Kind) bool {
	ts, ok := a.tracks[k]
	return ok && !ts.outOfSync
}

// Stats returns counters.
func (a *Aligner) Stats() Stats { return a.st }

// AdjustOffset asks the aligner to shift a track's alignment by delta ticks.
// A delta within the correction budget is applied and the track returns to
// sync. A larger delta is refused: the alignment is unchanged, the track is
// marked out of sync, and an OutOfSync error is returned. Callers that know
// the shift is legitimate must re-register the track instead, which makes the
// change an intentional re-baseline rather than a drift correction.
func (a *Aligner) AdjustOffset(kind Kind, delta int64) error {
	ts, ok := a.tracks[kind]
	if !ok {
		return merrors.New(merrors.StageSync, merrors.Unsupported, "track %q not registered", kind)
	}
	mag := absI64(delta)
	if mag > a.cfg.MaxCorrectionTicks {
		a.st.Refused++
		ts.outOfSync = true
		return merrors.New(merrors.StageSync, merrors.OutOfSync,
			"correction %d ticks exceeds limit %d for %s", delta, a.cfg.MaxCorrectionTicks, kind)
	}
	ts.adjust += delta
	ts.outOfSync = false
	a.st.MaxCorrection = maxU64(a.st.MaxCorrection, mag)
	return nil
}

// Align maps one sample onto the shared clock.
//
// A registered, in-sync track gets CommonTick = Extended + base + adjustment.
// A sample that would move backwards by more than the correction budget also
// puts the track out of sync: a timeline that regresses by that much is a
// clock fault, not jitter.
//
// The returned error, when non-nil, describes why the sample is out of sync;
// the sample itself is always returned so callers can keep the media data.
func (a *Aligner) Align(s Sample) (Aligned, error) {
	ts, ok := a.tracks[s.Track]
	if !ok {
		return Aligned{Sample: s, CommonTick: s.Extended},
			merrors.Packet(merrors.StageSync, merrors.Unsupported, s.SSRC, 0,
				"track %q not registered", s.Track)
	}
	if ts.track.SSRC != 0 && s.SSRC != ts.track.SSRC {
		return Aligned{Sample: s, CommonTick: s.Extended},
			merrors.Packet(merrors.StageSync, merrors.Invalid, s.SSRC, 0,
				"track %q expects SSRC %d", s.Track, ts.track.SSRC)
	}

	base := ts.track.OffsetTicks + ts.adjust
	if ts.outOfSync {
		a.st.OutOfSync++
		return Aligned{Sample: s, CommonTick: s.Extended, Correction: base, InSync: false},
			merrors.Packet(merrors.StageSync, merrors.OutOfSync, s.SSRC, 0,
				"track %s is out of sync; original timestamp retained", s.Track)
	}

	// Bring the track-local tick count into reference ticks before applying
	// the offset, so tracks with different clock rates are comparable.
	scaled := scaleTicks(s.Extended, ts.track.ClockRate, a.cfg.ReferenceRate)
	common, ok := applyOffset(scaled, base)
	if !ok {
		ts.outOfSync = true
		a.st.OutOfSync++
		return Aligned{Sample: s, CommonTick: s.Extended, Correction: base, InSync: false},
			merrors.Packet(merrors.StageSync, merrors.OutOfSync, s.SSRC, 0,
				"offset %d underflows timestamp %d", base, s.Extended)
	}

	if ts.haveLast && common < ts.lastCommon {
		back := ts.lastCommon - common
		if back > a.cfg.MaxCorrectionTicks {
			ts.outOfSync = true
			a.st.OutOfSync++
			return Aligned{Sample: s, CommonTick: s.Extended, Correction: base, InSync: false},
				merrors.Packet(merrors.StageSync, merrors.OutOfSync, s.SSRC, 0,
					"timeline regressed %d ticks on %s, beyond limit %d",
					back, s.Track, a.cfg.MaxCorrectionTicks)
		}
		if back > a.st.MaxCorrection {
			a.st.MaxCorrection = back
		}
	}

	ts.haveLast = true
	ts.lastCommon = common
	a.st.Aligned++
	return Aligned{Sample: s, CommonTick: common, Correction: base, InSync: true}, nil
}

// Skew returns the signed offset needed to move b onto a's clock, in ticks,
// and whether it lies within the correction budget. It is the inter-track
// measure the pipeline reports for a pair of concurrently captured samples.
func (a *Aligner) Skew(audio, video Aligned) (int64, bool) {
	delta := int64(audio.CommonTick) - int64(video.CommonTick)
	return delta, absI64(delta) <= a.cfg.MaxCorrectionTicks
}

// Reset clears a track's accumulated correction and sync state.
func (a *Aligner) Reset(kind Kind) {
	if ts, ok := a.tracks[kind]; ok {
		ts.adjust = 0
		ts.outOfSync = false
		ts.haveLast = false
	}
}

// String renders configuration for diagnostics.
func (a *Aligner) String() string {
	return fmt.Sprintf("aligner tracks=%d maxCorrection=%d aligned=%d outOfSync=%d refused=%d",
		len(a.tracks), a.cfg.MaxCorrectionTicks, a.st.Aligned, a.st.OutOfSync, a.st.Refused)
}

// scaleTicks converts a tick count from one clock rate to another, rounding
// down. Integer arithmetic only: repeated conversions cannot accumulate
// floating point error.
func scaleTicks(ticks uint64, from, to uint32) uint64 {
	if from == to || from == 0 {
		return ticks
	}
	// Split to avoid overflowing ticks*to for large timelines.
	whole := ticks / uint64(from)
	rem := ticks % uint64(from)
	return whole*uint64(to) + rem*uint64(to)/uint64(from)
}

func applyOffset(v uint64, off int64) (uint64, bool) {
	if off >= 0 {
		sum := v + uint64(off)
		if sum < v {
			return 0, false
		}
		return sum, true
	}
	mag := uint64(-off)
	if v < mag {
		return 0, false
	}
	return v - mag, true
}

func absI64(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
