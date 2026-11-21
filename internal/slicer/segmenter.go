// Package slicer turns a parsed MPEG-TS elementary stream into HLS
// segments cut only on video keyframes, while tracking segment timing and
// maintaining a bounded rolling window.
package slicer

import (
	"time"

	"streamcheck/internal/clock"
)

// WindowConfig controls segmentation cadence and retention.
type WindowConfig struct {
	// TargetSegment is the desired segment duration; cuts are delayed to
	// the next keyframe and the drift is recorded.
	TargetSegment time.Duration
	// WindowSize is the number of segments retained in a live playlist;
	// zero means every segment is kept (VOD behaviour).
	WindowSize int
}

// SegmentRecord is one emitted segment's bookkeeping.
type SegmentRecord struct {
	Sequence uint64
	File     string
	StartPTS uint64 // expanded 90 kHz ticks
	EndPTS   uint64
	Duration time.Duration
	Keyframe bool
	// DriftMS records how far the actual cut sits from the ideal boundary
	// because a keyframe was not available exactly on time.
	DriftMS int64
}

// Window holds the rolling segment state for one variant stream.
type Window struct {
	cfg        WindowConfig
	records    []SegmentRecord
	nextSeq    uint64
	firstPTS   uint64
	hasFirst   bool
	lastPTS    uint64
	unw        clock.Unwrapper
	openStart  uint64
	openHasPTS bool
}

// NewWindow creates a window with the given configuration.
func NewWindow(cfg WindowConfig) *Window {
	if cfg.TargetSegment <= 0 {
		cfg.TargetSegment = 3 * time.Second
	}
	return &Window{cfg: cfg}
}

// ObservePTS feeds a raw PTS and returns the expanded value plus the
// classified jump so callers can surface wraps and restarts.
func (w *Window) ObservePTS(raw uint64) (uint64, Jump) {
	exp, kind := w.unw.Feed(raw)
	w.lastPTS = exp
	if !w.hasFirst {
		w.hasFirst = true
		w.firstPTS = exp
	}
	return exp, Jump(kind)
}

// Jump re-exports the clock jump classification for slicer callers.
type Jump = clock.JumpKind

// ShouldCut decides whether an observed keyframe should close the open
// segment. It returns true once the accumulated PTS span reaches the target.
func (w *Window) ShouldCut(keyframe bool, nowPTS uint64) bool {
	if !keyframe || !w.openHasPTS {
		return false
	}
	span := nowPTS - w.openStart
	return clock.Duration(span) >= w.cfg.TargetSegment
}

// OpenSegment records the starting PTS of a new segment at a keyframe.
func (w *Window) OpenSegment(startPTS uint64) {
	w.openStart = startPTS
	w.openHasPTS = true
}

// CloseSegment finalises a segment at endPTS and returns its record. The
// drift versus the ideal target duration is computed for diagnostics.
func (w *Window) CloseSegment(file string, endPTS uint64, keyframe bool) SegmentRecord {
	dur := clock.Duration(endPTS - w.openStart)
	drift := dur - w.cfg.TargetSegment
	rec := SegmentRecord{
		Sequence: w.nextSeq,
		File:     file,
		StartPTS: w.openStart,
		EndPTS:   endPTS,
		Duration: dur,
		Keyframe: keyframe,
		DriftMS:  drift.Milliseconds(),
	}
	w.nextSeq++
	w.records = append(w.records, rec)
	w.openHasPTS = false
	if w.cfg.WindowSize > 0 && len(w.records) > w.cfg.WindowSize {
		w.records = w.records[len(w.records)-w.cfg.WindowSize:]
	}
	return rec
}

// Records returns the retained segment records in sequence order.
func (w *Window) Records() []SegmentRecord { return w.records }

// NextSequence returns the sequence number the next segment will receive.
func (w *Window) NextSequence() uint64 { return w.nextSeq }

// MaxAbsDriftMS returns the largest absolute cut drift seen so far.
func (w *Window) MaxAbsDriftMS() int64 {
	var max int64
	for _, r := range w.records {
		d := r.DriftMS
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
	}
	return max
}

// TotalDuration sums the retained segment durations.
func (w *Window) TotalDuration() time.Duration {
	var total time.Duration
	for _, r := range w.records {
		total += r.Duration
	}
	return total
}
