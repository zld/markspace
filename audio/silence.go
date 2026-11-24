// Package audio analyses project-defined elementary-stream payloads as
// sample windows. It does not decode any codec: payload bytes are treated as
// unsigned sample magnitudes, which is sufficient for the silence statistics
// this project promises. All windows and intervals use the shared 90 kHz
// timeline and structured range errors.
package audio

import (
	"mediad/internal/clock"
	"mediad/ts"
)

// Interval is one detected silence span in monotonic 90 kHz ticks.
type Interval struct {
	TrackID string
	Start   int64
	End     int64
}

// StartMs / EndMs expose the boundaries rounded to whole milliseconds.
func (i Interval) StartMs() int64 { return clock.Ms(i.Start) }
func (i Interval) EndMs() int64   { return clock.Ms(i.End) }

// DetectorConfig parameterises silence detection.
type DetectorConfig struct {
	// Threshold is the maximum mean sample magnitude considered silent,
	// on the 0..255 scale of an unsigned payload byte.
	Threshold float64
	// MinDurationTicks is the shortest silence worth reporting.
	MinDurationTicks int64
}

// Detector consumes track events and emits per-track silence intervals.
type Detector struct {
	cfg DetectorConfig
	// open tracks the currently accumulating silent window per track.
	open map[string]*Interval
	// blocked marks tracks whose window touches an unhandled error.
	blocked map[string]bool
	out     []Interval
	errs    []clock.RangeError
}

// NewDetector validates and constructs a detector.
func NewDetector(cfg DetectorConfig) (*Detector, error) {
	if cfg.Threshold < 0 || cfg.Threshold > 255 {
		return nil, errSilence("silence threshold must be within 0..255")
	}
	if cfg.MinDurationTicks < 0 {
		return nil, errSilence("min duration must not be negative")
	}
	return &Detector{
		cfg:     cfg,
		open:    map[string]*Interval{},
		blocked: map[string]bool{},
	}, nil
}

type silenceError string

func (e silenceError) Error() string { return string(e) }
func errSilence(msg string) error    { return silenceError(msg) }

// Errors returns the structured errors the detector observed (all of them are
// shared by reference from the TS timeline/PSI/continuity stages).
func (d *Detector) Errors() []clock.RangeError { return d.errs }

// Observe feeds one track event together with the errors known at its time.
// A window that overlaps any error range is closed at the error boundary and
// never reported across it, so slicing cannot span an unhandled error.
func (d *Detector) Observe(ev ts.TrackEvent, errs []clock.RangeError) {
	for _, e := range errs {
		if e.Kind == clock.KindContinuity || e.Kind == clock.KindDiscontinuity ||
			e.Kind == clock.KindPCRWrap || e.Kind == clock.KindSync {
			if e.Start < ev.End && ev.Start < e.End {
				d.closeAt(ev.TrackID, e.Start)
				d.blocked[ev.TrackID] = true
				d.errs = append(d.errs, e)
			}
		}
	}
	silent := meanMagnitude(ev.Payload) <= d.cfg.Threshold
	switch {
	case silent && !d.blocked[ev.TrackID]:
		if w, ok := d.open[ev.TrackID]; ok {
			w.End = ev.End
		} else {
			d.open[ev.TrackID] = &Interval{TrackID: ev.TrackID, Start: ev.Start, End: ev.End}
		}
	case !silent:
		d.closeAt(ev.TrackID, ev.Start)
		d.blocked[ev.TrackID] = false
	}
}

func (d *Detector) closeAt(track string, boundary int64) {
	w, ok := d.open[track]
	if !ok {
		return
	}
	if boundary > w.Start && boundary < w.End {
		w.End = boundary // error boundary trims the open window
	}
	if w.End-w.Start >= d.cfg.MinDurationTicks {
		d.out = append(d.out, *w)
	}
	delete(d.open, track)
}

// Flush closes any open window at the end of the stream and returns the
// intervals that satisfy the minimum-duration requirement.
func (d *Detector) Flush() []Interval {
	for track, w := range d.open {
		if w.End-w.Start >= d.cfg.MinDurationTicks {
			d.out = append(d.out, *w)
		}
		delete(d.open, track)
	}
	return d.out
}

// meanMagnitude averages unsigned payload bytes; empty payloads are silent.
func meanMagnitude(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var sum int
	for _, x := range b {
		sum += int(x)
	}
	return float64(sum) / float64(len(b))
}
