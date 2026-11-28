// Package sync aligns subtitle cues with container and RTP anchors on the
// shared nanosecond timeline.
//
// Alignment never moves a cue. When the measured error exceeds the configured
// tolerance the pair is reported as a drift and the cue keeps its own timestamp,
// so a reviewer can see exactly where the recovered media diverges.
package sync

import (
	"mediarc/model"
)

const stream = "sync"

// DefaultTolerance is the maximum permitted alignment error (1 ms).
const DefaultTolerance int64 = 1_000_000

// Anchor is a reference point on the timeline, typically derived from a
// container sample or an RTP segment start.
type Anchor struct {
	// TimeNS is the reference time.
	TimeNS int64
	// Label identifies the anchor in reports.
	Label string
}

// CueRef is one subtitle cue to align.
type CueRef struct {
	// Index names the cue in reports.
	Index int
	// StartNS and EndNS are the cue's own times, untouched by alignment.
	StartNS int64
	EndNS   int64
}

// Pair is the alignment outcome for one cue.
type Pair struct {
	Cue    CueRef
	Anchor Anchor
	// ErrorNS is the signed offset of the cue start from the anchor: positive
	// means the cue starts after the anchor.
	ErrorNS int64
	// AbsError is |ErrorNS|.
	AbsError int64
	// WithinTolerance is true when AbsError <= tolerance.
	WithinTolerance bool
	// Issue is set when the pair is outside tolerance.
	Issue *model.Error
}

// Config controls alignment.
type Config struct {
	// ToleranceNS is the maximum allowed absolute error. Zero means
	// DefaultTolerance; negative values are rejected.
	ToleranceNS int64
	// RequireAnchor makes a cue with no anchor at all an error. Without it,
	// unanchored cues are reported but not flagged.
	RequireAnchor bool
}

func (c Config) validate() (Config, error) {
	if c.ToleranceNS < 0 {
		return Config{}, model.New(model.CodeInvalidArgument, stream, "tolerance must not be negative")
	}
	if c.ToleranceNS == 0 {
		c.ToleranceNS = DefaultTolerance
	}
	return c, nil
}

// Result is the whole alignment report.
type Result struct {
	Pairs []Pair
	// MaxErrorNS is the largest absolute error among aligned pairs.
	MaxErrorNS int64
	// WithinTolerance counts pairs inside the tolerance.
	WithinTolerance int
	// Drifted counts pairs outside the tolerance.
	Drifted int
	// Unanchored counts cues that found no anchor.
	Unanchored int
	// Issues lists every drift or missing-anchor error in cue order.
	Issues []*model.Error
}

// OK reports whether every cue aligned within tolerance.
func (r Result) OK() bool { return r.Drifted == 0 && len(r.Issues) == 0 }

// Aligner aligns cues to anchors.
type Aligner struct {
	cfg Config
}

// New validates cfg.
func New(cfg Config) (*Aligner, error) {
	validated, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &Aligner{cfg: validated}, nil
}

// Tolerance reports the effective tolerance.
func (a *Aligner) Tolerance() int64 { return a.cfg.ToleranceNS }
