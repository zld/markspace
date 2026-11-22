package clock

import (
	"fmt"
	"sort"

	"mediad/internal/core"
)

// TrackKind distinguishes the media tracks that share the timeline.
type TrackKind string

const (
	KindVideo    TrackKind = "video"
	KindAudio    TrackKind = "audio"
	KindSubtitle TrackKind = "subtitle"
	KindTimecode TrackKind = "timecode"
	KindRTP      TrackKind = "rtp"
)

// Track is one input clock that must be normalized onto the master timeline.
type Track struct {
	Name    string        `json:"name"`
	Kind    TrackKind     `json:"kind"`
	Asset   string        `json:"asset"`
	Scale   core.Rational `json:"scale"`  // seconds per native tick
	Origin  core.Rational `json:"origin"` // master seconds at native tick 0
	Drift   *DriftModel   `json:"drift,omitempty"`
	First   core.Rational `json:"first"`
	Last    core.Rational `json:"last"`
	Samples int           `json:"samples"`
}

// Point is one normalized instant. Native retains the unadjusted input clock
// in the track's own ticks; Master is the negotiated shared clock in seconds.
// Missing marks a point skipped because its input bytes were unavailable.
type Point struct {
	Track   string        `json:"track"`
	Kind    TrackKind     `json:"kind"`
	Native  core.Rational `json:"native"`
	Master  core.Rational `json:"master"`
	Wrapped bool          `json:"wrapped"`
	Clamped bool          `json:"clamped"`
	Span    core.Span     `json:"span"`
}

// Timeline is the shared master clock after normalization.
type Timeline struct {
	Master   string           `json:"master"`
	Tracks   []Track          `json:"tracks"`
	Points   []Point          `json:"points"`
	Wraps    int              `json:"wraps"`
	Clamps   int              `json:"clamps"`
	DriftPPB map[string]int64 `json:"drift_ppb,omitempty"`
}

// Builder accumulates per-track normalized points. Tracks are normalized
// against an explicitly chosen master track so results do not depend on
// iteration order.
type Builder struct {
	master   string
	tracks   []Track
	points   []Point
	wraps    int
	clamps   int
	driftPPB map[string]int64
	correct  map[string]*DriftCorrector
	sealed   bool
}

// NewBuilder starts a timeline whose reference is the named track.
func NewBuilder(master string) *Builder {
	return &Builder{
		master:   master,
		driftPPB: make(map[string]int64),
		correct:  make(map[string]*DriftCorrector),
	}
}

// AddTrack registers a track and its scale/origin contract.
func (b *Builder) AddTrack(t Track) error {
	if b.sealed {
		return fmt.Errorf("clock: timeline already sealed")
	}
	if t.Name == "" || t.Asset == "" {
		return fmt.Errorf("clock: track needs a name and asset")
	}
	if t.Scale.Num <= 0 {
		return fmt.Errorf("clock: track %s has non-positive scale", t.Name)
	}
	for _, e := range b.tracks {
		if e.Name == t.Name {
			return fmt.Errorf("clock: duplicate track %s", t.Name)
		}
	}
	if t.Drift != nil {
		if err := t.Drift.Validate(); err != nil {
			return fmt.Errorf("clock: track %s: %w", t.Name, err)
		}
	}
	b.tracks = append(b.tracks, t)
	if t.Drift != nil && t.Drift.Applied {
		b.correct[t.Name] = NewCorrector(*t.Drift)
		b.driftPPB[t.Name] = t.Drift.RatePPB
	}
	return nil
}

// AddPoint normalizes one native instant for a registered track.
func (b *Builder) AddPoint(track string, native core.Rational, wrapped bool, span core.Span) (Point, error) {
	if b.sealed {
		return Point{}, fmt.Errorf("clock: timeline already sealed")
	}
	var tr *Track
	for i := range b.tracks {
		if b.tracks[i].Name == track {
			tr = &b.tracks[i]
			break
		}
	}
	if tr == nil {
		return Point{}, fmt.Errorf("clock: unregistered track %q", track)
	}
	master := tr.Origin.Add(native.Mul(tr.Scale))
	p := Point{Track: track, Kind: tr.Kind, Native: native, Master: master, Wrapped: wrapped, Span: span}
	if c, ok := b.correct[track]; ok {
		p.Master, p.Clamped = c.Correct(master, wrapped)
		if p.Clamped {
			b.clamps++
		}
	}
	if wrapped {
		b.wraps++
	}
	b.points = append(b.points, p)
	return p, nil
}

// Seal sorts points by master clock and verifies per-track monotonicity.
// It returns an error describing the first regression so callers can attach
// the offending span to a diagnostic.
func (b *Builder) Seal() (*Timeline, error) {
	if b.sealed {
		return nil, fmt.Errorf("clock: timeline already sealed")
	}
	b.sealed = true
	sort.SliceStable(b.points, func(i, j int) bool {
		if b.points[i].Master.Cmp(b.points[j].Master) != 0 {
			return b.points[i].Master.Cmp(b.points[j].Master) < 0
		}
		if b.points[i].Track != b.points[j].Track {
			return b.points[i].Track < b.points[j].Track
		}
		return b.points[i].Span.Start < b.points[j].Span.Start
	})
	last := make(map[string]core.Rational, len(b.tracks))
	for _, p := range b.points {
		if prev, ok := last[p.Track]; ok && p.Master.Cmp(prev) < 0 {
			return nil, &RegressionError{Track: p.Track, Previous: prev, Current: p.Master, Span: p.Span}
		}
		last[p.Track] = p.Master
	}
	for i := range b.tracks {
		for _, p := range b.points {
			if p.Track != b.tracks[i].Name {
				continue
			}
			b.tracks[i].Samples++
			if b.tracks[i].Samples == 1 {
				b.tracks[i].First = p.Master
			}
			b.tracks[i].Last = p.Master
		}
	}
	tl := &Timeline{
		Master: b.master,
		Tracks: append([]Track(nil), b.tracks...),
		Points: append([]Point(nil), b.points...),
		Wraps:  b.wraps,
		Clamps: b.clamps,
	}
	if len(b.driftPPB) > 0 {
		tl.DriftPPB = make(map[string]int64, len(b.driftPPB))
		for k, v := range b.driftPPB {
			tl.DriftPPB[k] = v
		}
	}
	return tl, nil
}

// RegressionError reports a normalized clock that moved backwards.
type RegressionError struct {
	Track    string
	Previous core.Rational
	Current  core.Rational
	Span     core.Span
}

func (e *RegressionError) Error() string {
	return fmt.Sprintf("clock: track %s regressed %s -> %s at [%d,%d)", e.Track, e.Previous, e.Current, e.Span.Start, e.Span.End)
}

// Span returns the master clock interval covered by all points.
func (t *Timeline) Span() (start, end core.Rational) {
	if len(t.Points) == 0 {
		return core.Zero(), core.Zero()
	}
	return t.Points[0].Master, t.Points[len(t.Points)-1].Master
}

// TrackByName returns the registered track or nil.
func (t *Timeline) TrackByName(name string) *Track {
	for i := range t.Tracks {
		if t.Tracks[i].Name == name {
			return &t.Tracks[i]
		}
	}
	return nil
}

// Validate checks structural invariants of a sealed timeline.
func (t *Timeline) Validate() error {
	if t.Master == "" {
		return fmt.Errorf("clock: timeline has no master track")
	}
	if t.TrackByName(t.Master) == nil {
		return fmt.Errorf("clock: master track %q is not registered", t.Master)
	}
	last := make(map[string]core.Rational)
	var prevMaster core.Rational
	for i, p := range t.Points {
		if t.TrackByName(p.Track) == nil {
			return fmt.Errorf("clock: point %d references unknown track %q", i, p.Track)
		}
		if p.Span.Start < 0 || p.Span.End < p.Span.Start {
			return fmt.Errorf("clock: point %d has invalid span", i)
		}
		if prev, ok := last[p.Track]; ok && p.Master.Cmp(prev) < 0 {
			return fmt.Errorf("clock: track %s is not monotonic", p.Track)
		}
		if i > 0 && p.Master.Cmp(prevMaster) < 0 {
			return fmt.Errorf("clock: master clock is not monotonic")
		}
		last[p.Track] = p.Master
		prevMaster = p.Master
	}
	return nil
}
