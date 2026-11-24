// Package clock provides a unified media timeline that converts wrapped
// 32-bit RTP timestamps into a monotonically increasing media clock and
// anchors that clock to wall time using RTCP Sender Reports.
package clock

import (
	"errors"
	"fmt"
	"time"
)

// ClockRate is the RTP media clock rate for video (90 kHz).
const ClockRate = 90000

// ErrNoAnchor is returned when no RTCP Sender Report anchor is available,
// so absolute media time cannot be established.
var ErrNoAnchor = errors.New("clock: no RTCP sender-report anchor")

// Extender converts wrapped uint32 RTP timestamps into extended int64
// timestamps that never go backwards across 32-bit wrap boundaries.
type Extender struct {
	cycles int64 // accumulated wrap offset (multiples of 1<<32)
	last   int64 // last extended value
	init   bool
}

// Extend maps a raw RTP timestamp onto the extended timeline. Jumps of more
// than half the wrap range are interpreted as a wrap (forward) or as a late
// out-of-order packet (backward), which keeps the extended value stable.
func (e *Extender) Extend(ts uint32) int64 {
	const halfRange = int64(1) << 31
	if !e.init {
		e.init = true
		e.last = int64(ts)
		return e.last
	}
	cand := int64(ts) + e.cycles
	switch {
	case cand-e.last > halfRange:
		e.cycles -= 1 << 32
		cand -= 1 << 32
	case e.last-cand > halfRange:
		e.cycles += 1 << 32
		cand += 1 << 32
	}
	if cand > e.last {
		e.last = cand
	}
	return cand
}

// Anchor binds one RTP timestamp to an NTP wall-clock instant.
type Anchor struct {
	RTP uint32
	NTP time.Time
}

// Timeline maintains the RTP->wall-clock mapping from RTCP Sender Reports
// and estimates clock drift between the sender and the 90 kHz media clock.
type Timeline struct {
	ext      Extender
	anchors  []Anchor
	driftPPM float64 // estimated sender clock drift, parts per million
}

// NewTimeline returns an empty timeline with no anchors.
func NewTimeline() *Timeline { return &Timeline{} }

// AddSenderReport records an RTCP SR anchor (RTP timestamp <-> NTP time).
// With two or more anchors the sender clock drift is estimated.
func (t *Timeline) AddSenderReport(rtpTS uint32, ntp time.Time) {
	t.anchors = append(t.anchors, Anchor{RTP: rtpTS, NTP: ntp})
	if n := len(t.anchors); n >= 2 {
		a, b := t.anchors[n-2], t.anchors[n-1]
		dRTP := float64(int64(b.RTP) - int64(a.RTP))
		// handle wrap between anchors
		if dRTP < 0 {
			dRTP += 1 << 32
		}
		dNTP := b.NTP.Sub(a.NTP).Seconds()
		if dNTP > 0 && dRTP > 0 {
			expected := dNTP * ClockRate
			t.driftPPM = (dRTP - expected) / expected * 1e6
		}
	}
}

// Anchored reports whether at least one anchor exists.
func (t *Timeline) Anchored() bool { return len(t.anchors) > 0 }

// DriftPPM returns the estimated sender clock drift in ppm (0 if unknown).
func (t *Timeline) DriftPPM() float64 { return t.driftPPM }

// Extend converts a raw RTP timestamp to the extended media timeline.
func (t *Timeline) Extend(ts uint32) int64 { return t.ext.Extend(ts) }

// MediaMs converts an extended RTP timestamp to milliseconds on the media
// clock relative to the first anchor. The boolean is false when no anchor
// exists and the time is therefore uncertain.
func (t *Timeline) MediaMs(extTS int64) (float64, bool) {
	if !t.Anchored() {
		return 0, false
	}
	a := t.anchors[0]
	base := int64(a.RTP)
	// align base with the cycles of extTS
	for base+1<<31 < extTS {
		base += 1 << 32
	}
	for base-1<<31 > extTS {
		base -= 1 << 32
	}
	return float64(extTS-base) / ClockRate * 1000, true
}

// WallTime converts an extended RTP timestamp to wall-clock time using the
// nearest anchor, compensating for estimated drift.
func (t *Timeline) WallTime(extTS int64) (time.Time, bool) {
	if !t.Anchored() {
		return time.Time{}, false
	}
	a := t.anchors[len(t.anchors)-1]
	base := int64(a.RTP)
	for base+1<<31 < extTS {
		base += 1 << 32
	}
	for base-1<<31 > extTS {
		base -= 1 << 32
	}
	deltaTicks := float64(extTS - base)
	// compensate drift: sender clock runs (1+drift) relative to wall
	sec := deltaTicks / ClockRate / (1 + t.driftPPM/1e6)
	return a.NTP.Add(time.Duration(sec * float64(time.Second))), true
}

// String summarizes the timeline state for diagnostics.
func (t *Timeline) String() string {
	return fmt.Sprintf("anchors=%d drift_ppm=%.1f", len(t.anchors), t.driftPPM)
}
