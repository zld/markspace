package ingest

import (
	"fmt"

	"mediacore/media"
)

// ColorMeta is one observation of the stream's color metadata. Fields are
// tracked verbatim; the kernel never invents defaults for absent values.
type ColorMeta struct {
	Primaries string // e.g. "bt709", "bt2020", "" if absent
	Transfer  string // e.g. "bt709", "pq", "" if absent
	Matrix    string // e.g. "bt709", "bt2020nc", "" if absent
	FullRange *bool  // nil = unspecified
}

// Complete reports whether every field is present.
func (c ColorMeta) Complete() bool {
	return c.Primaries != "" && c.Transfer != "" && c.Matrix != "" && c.FullRange != nil
}

// Equal reports field-by-field equality (nil FullRange only equals nil).
func (c ColorMeta) Equal(o ColorMeta) bool {
	if c.Primaries != o.Primaries || c.Transfer != o.Transfer || c.Matrix != o.Matrix {
		return false
	}
	if (c.FullRange == nil) != (o.FullRange == nil) {
		return false
	}
	return c.FullRange == nil || *c.FullRange == *o.FullRange
}

// ColorState is the explicit lifecycle of color metadata for a stream.
type ColorState int

const (
	ColorMissing  ColorState = iota // never observed; consumers must not assume
	ColorStable                     // observed, consistent
	ColorChanged                    // changed exactly once, consistently since
	ColorConflict                   // contradictory values at the same position
)

func (s ColorState) String() string {
	switch s {
	case ColorMissing:
		return "missing"
	case ColorStable:
		return "stable"
	case ColorChanged:
		return "changed"
	case ColorConflict:
		return "conflict"
	}
	return "unknown"
}

// ColorTracker folds a time-ordered series of color observations into an
// explicit state. A conflict (two different values for the same sample
// position) is sticky and propagates to the output summary.
type ColorTracker struct {
	state   ColorState
	current ColorMeta
	changes int
}

// Observe records one observation at a stream position. Two different
// metas at the same position are a conflict; a new value at a later
// position is a change. Returns the state after folding.
func (t *ColorTracker) Observe(m ColorMeta, pos int64, prevPos int64) ColorState {
	switch {
	case t.state == ColorConflict:
		// sticky
	case t.state == ColorMissing:
		t.current, t.state = m, ColorStable
	case pos == prevPos && !m.Equal(t.current):
		t.state = ColorConflict
	case !m.Equal(t.current):
		t.current, t.state = m, ColorChanged
		t.changes++
	}
	return t.state
}

// State returns the folded state.
func (t *ColorTracker) State() ColorState { return t.state }

// Summary renders the state for the output digest. A conflict is a
// classified error so it propagates rather than being silently averaged.
func (t *ColorTracker) Summary() (string, error) {
	switch t.state {
	case ColorConflict:
		return "conflict", media.Fault(media.CodeColorConflict, "ingest.ColorTracker", -1,
			"contradictory color metadata observed")
	case ColorMissing:
		return "missing", nil
	default:
		c := t.current
		rng := "unspecified"
		if c.FullRange != nil {
			rng = fmt.Sprintf("%v", *c.FullRange)
		}
		return fmt.Sprintf("%s|%s/%s/%s/range=%s", t.state, c.Primaries, c.Transfer, c.Matrix, rng), nil
	}
}

// AudioMeta declares the capture-side audio format.
type AudioMeta struct {
	Rate     int    // samples per second, e.g. 44100, 48000
	Channels int    // 1, 2, or 6
	Layout   string // "mono", "stereo", "5.1"; "" = undeclared
}

// Validate rejects inconsistent audio declarations.
func (a AudioMeta) Validate() error {
	const op = "ingest.AudioMeta"
	if a.Rate != 44100 && a.Rate != 48000 {
		return media.Fault(media.CodeLayoutUnknown, op, -1, "unsupported sample rate %d", a.Rate)
	}
	want := map[string]int{"mono": 1, "stereo": 2, "5.1": 6}
	n, ok := want[a.Layout]
	if !ok {
		return media.Fault(media.CodeLayoutUnknown, op, -1, "undeclared layout %q", a.Layout)
	}
	if a.Channels != n {
		return media.Fault(media.CodeLayoutUnknown, op, -1,
			"layout %q implies %d channels, got %d", a.Layout, n, a.Channels)
	}
	return nil
}
