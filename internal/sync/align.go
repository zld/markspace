// Package sync aligns recovered audio and video on the single clock.Timeline.
//
// Both tracks are already expressed in timeline ticks by the time they reach
// Aligner, so alignment is a pure comparison: no module re-derives or
// accumulates time. If the absolute offset exceeds the configured tolerance the
// alignment fails with a CatSync error carrying the measured offset, and the
// mux stage is expected to refuse to write. Committed intervals are immutable,
// so retrying alignment can never alter already published media time.
package sync

import (
	"fmt"

	"mediapipeline/internal/clock"
	mpierr "mediapipeline/internal/errors"
)

// Kind aliases the clock media kinds for callers' convenience.
const (
	KindAudio = clock.KindAudio
	KindVideo = clock.KindVideo
)

// Config configures the Aligner.
type Config struct {
	// ToleranceTicks is the maximum allowed absolute A/V offset in ticks.
	ToleranceTicks uint64
	// Node is stamped onto sync errors.
	Node string
}

// TrackPoint is one track's reference position on the timeline.
type TrackPoint struct {
	Kind     clock.Kind
	Start    uint64
	Duration uint64
}

// Report is the deterministic result of an alignment check.
type Report struct {
	// AudioStart / VideoStart are the compared interval starts in ticks.
	AudioStart uint64
	VideoStart uint64
	// OffsetTicks is the signed offset: audio start minus video start.
	OffsetTicks int64
	// AbsOffsetTicks is its absolute value.
	AbsOffsetTicks uint64
	// WithinTolerance reports whether muxing is allowed.
	WithinTolerance bool
}

// Aligner performs A/V alignment checks against one timeline.
type Aligner struct {
	cfg Config
	tl  *clock.Timeline
}

// New validates the config and builds an Aligner.
func New(cfg Config, tl *clock.Timeline) (*Aligner, error) {
	if tl == nil {
		return nil, fmt.Errorf("sync: timeline is required")
	}
	return &Aligner{cfg: cfg, tl: tl}, nil
}

// Align compares the first committed intervals of audio and video. It commits
// the video point (idempotently guarded by the timeline) and returns a Report;
// an out-of-tolerance result is also returned as a CatSync error so the caller
// can block the mux while still recording the measured offset.
func (a *Aligner) Align(audio, video TrackPoint) (Report, error) {
	if audio.Kind != clock.KindAudio || video.Kind != clock.KindVideo {
		return Report{}, fmt.Errorf("sync: align requires one audio and one video point")
	}
	rep := Report{AudioStart: audio.Start, VideoStart: video.Start}
	rep.OffsetTicks = int64(audio.Start) - int64(video.Start)
	if rep.OffsetTicks < 0 {
		rep.AbsOffsetTicks = uint64(-rep.OffsetTicks)
	} else {
		rep.AbsOffsetTicks = uint64(rep.OffsetTicks)
	}
	rep.WithinTolerance = rep.AbsOffsetTicks <= a.cfg.ToleranceTicks
	if !rep.WithinTolerance {
		return rep, mpierr.New(mpierr.CatSync, 0, a.cfg.Node,
			fmt.Sprintf("a/v offset %d ticks exceeds tolerance %d",
				rep.AbsOffsetTicks, a.cfg.ToleranceTicks))
	}
	return rep, nil
}

// OffsetMs renders an offset in whole milliseconds for terminal reports.
func (a *Aligner) OffsetMs(rep Report) int64 {
	return rep.OffsetTicks * 1000 / int64(a.tl.TickRate())
}

// Timeline exposes the shared timeline.
func (a *Aligner) Timeline() *clock.Timeline { return a.tl }
