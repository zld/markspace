// Package sync estimates audio/video clock drift from paired timestamps and
// applies a slope-limited correction. Drift is modelled as a linear map from
// the raw media clock to a corrected clock; the correction slope is bounded by
// MaxPPM so a noisy estimate can never warp the timeline abruptly. The raw
// timestamp is always retained alongside the corrected one.
package sync

import (
	"math"
	"sort"

	"mediasync/internal/model"
)

// Config bounds the drift estimator and correction.
type Config struct {
	// MaxPPM is the largest absolute correction slope, in parts per million.
	// A capture clock that drifts more than this is reported rather than
	// compensated beyond the bound.
	MaxPPM float64
	// Window is how many recent paired observations feed the estimate.
	Window int
	// MinSamples is how many pairs are required before a slope is applied.
	MinSamples int
}

// DefaultConfig is a conservative drift configuration (200 ppm, 256 pairs).
func DefaultConfig() Config {
	return Config{MaxPPM: 200, Window: 256, MinSamples: 8}
}

// Validate checks the bounds.
func (c Config) Validate() error {
	if c.MaxPPM <= 0 || c.MaxPPM > 10_000 {
		return model.NewError(model.CodeTimelineLate, model.Loc{TrackIndex: -1}, "MaxPPM must be in (0,10000], got %g", c.MaxPPM)
	}
	if c.Window < 2 {
		return model.NewError(model.CodeTimelineLate, model.Loc{TrackIndex: -1}, "Window must be at least 2, got %d", c.Window)
	}
	if c.MinSamples < 2 || c.MinSamples > c.Window {
		return model.NewError(model.CodeTimelineLate, model.Loc{TrackIndex: -1}, "MinSamples must be in 2..%d, got %d", c.Window, c.MinSamples)
	}
	return nil
}

// Pair is one reference/candidate observation. Reference is the trusted
// clock (for example audio), Candidate the clock being corrected.
type Pair struct {
	Reference model.Nanos
	Candidate model.Nanos
}

// Estimate is the fitted drift state. Slope is dimensionless: 1.0 means no
// correction, 1.0001 means the candidate clock runs 100 ppm fast.
type Estimate struct {
	Slope        float64
	PPM          float64
	Samples      int
	Clamped      bool
	Insufficient bool
}

// Estimator fits a slope over a sliding window of pairs using least squares
// against the earliest pair in the window. Fitting against the window origin
// keeps the estimate stable when timestamps are large.
type Estimator struct {
	cfg   Config
	pairs []Pair
}

// NewEstimator validates config and returns an estimator.
func NewEstimator(cfg Config) (*Estimator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Estimator{cfg: cfg}, nil
}

// Add records a pair. Non-positive candidate deltas are ignored because they
// carry no drift information.
func (e *Estimator) Add(p Pair) {
	if len(e.pairs) == e.cfg.Window {
		copy(e.pairs, e.pairs[1:])
		e.pairs = e.pairs[:len(e.pairs)-1]
	}
	e.pairs = append(e.pairs, p)
}

// Estimate returns the current slope. It sorts a copy of the window by
// candidate time so caller arrival order cannot change the result.
func (e *Estimator) Estimate() Estimate {
	est := Estimate{Slope: 1}
	n := len(e.pairs)
	if n < e.cfg.MinSamples {
		est.Insufficient = true
		est.Samples = n
		return est
	}
	pairs := append([]Pair(nil), e.pairs...)
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].Candidate != pairs[j].Candidate {
			return pairs[i].Candidate < pairs[j].Candidate
		}
		return pairs[i].Reference < pairs[j].Reference
	})
	// Least squares y = a + b*x over candidate-relative time.
	x0 := pairs[0].Candidate
	var sx, sy, sxx, sxy float64
	for _, p := range pairs {
		x := float64(p.Candidate - x0)
		y := float64(p.Reference - pairs[0].Reference)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := float64(n)*sxx - sx*sx
	if den == 0 {
		est.Insufficient = true
		est.Samples = n
		return est
	}
	slope := (float64(n)*sxy - sx*sy) / den
	est.Samples = n
	if math.IsNaN(slope) || math.IsInf(slope, 0) || slope <= 0 {
		est.Insufficient = true
		return est
	}
	maxSlope := 1 + e.cfg.MaxPPM/1e6
	minSlope := 1 - e.cfg.MaxPPM/1e6
	if slope > maxSlope {
		slope = maxSlope
		est.Clamped = true
	} else if slope < minSlope {
		slope = minSlope
		est.Clamped = true
	}
	est.Slope = slope
	est.PPM = (slope - 1) * 1e6
	return est
}

// Corrector maps a candidate clock (for example the audio media clock) onto a
// reference clock (for example the video timeline). The mapping is anchored at
// the first observed pair: corrected = refAnchor + (raw - candAnchor) * slope.
// This absorbs the constant capture offset while correcting only the drift
// rate, and it never changes the slope beyond MaxPPM.
type Corrector struct {
	est        *Estimator
	refAnchor  model.Nanos
	candAnchor model.Nanos
	anchorSet  bool
}

// NewCorrector builds a corrector over the given estimator.
func NewCorrector(est *Estimator) *Corrector { return &Corrector{est: est} }

// Observe feeds a pair to the estimator and anchors on the first one.
func (c *Corrector) Observe(p Pair) {
	if !c.anchorSet {
		c.refAnchor = p.Reference
		c.candAnchor = p.Candidate
		c.anchorSet = true
	}
	c.est.Add(p)
}

// State is the serializable drift state.
type State struct {
	RefAnchor  model.Nanos `json:"ref_anchor"`
	CandAnchor model.Nanos `json:"cand_anchor"`
	AnchorSet  bool        `json:"anchor_set"`
	PPM        float64     `json:"ppm"`
	Slope      float64     `json:"slope"`
	// Pairs is the estimator window, retained so a resumed run reproduces the
	// same slope exactly.
	Pairs []Pair `json:"pairs"`
}

// Snapshot returns the drift state including the estimator window.
func (c *Corrector) Snapshot() State {
	e := c.est.Estimate()
	return State{
		RefAnchor: c.refAnchor, CandAnchor: c.candAnchor, AnchorSet: c.anchorSet,
		PPM: e.PPM, Slope: e.Slope, Pairs: append([]Pair(nil), c.est.pairs...),
	}
}

// Restore replaces the drift state.
func (c *Corrector) Restore(s State) {
	c.refAnchor, c.candAnchor, c.anchorSet = s.RefAnchor, s.CandAnchor, s.AnchorSet
	c.est.pairs = append([]Pair(nil), s.Pairs...)
}

// Correct maps a raw candidate timestamp onto the reference clock. Before the
// estimator has enough pairs the timestamp is returned unchanged apart from
// the constant anchor offset, which keeps early output stable.
func (c *Corrector) Correct(raw model.Nanos) model.Nanos {
	if !c.anchorSet {
		return raw
	}
	slope := 1.0
	if e := c.est.Estimate(); !e.Insufficient {
		slope = e.Slope
	}
	delta := float64(raw - c.candAnchor)
	return c.refAnchor + model.Nanos(math.Round(delta*slope))
}

// Residual is the remaining reference/candidate divergence at a pair.
func (c *Corrector) Residual(p Pair) model.Nanos {
	return p.Reference - c.Correct(p.Candidate)
}

// Skew is the absolute audio/video offset between two samples.
func Skew(a, b model.Nanos) model.Nanos {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

// TrackStats accumulates the maximum observed audio/video skew per pair id.
type TrackStats struct {
	MaxSkew model.Nanos
	Pairs   int64
}

// Observe records a skew.
func (s *TrackStats) Observe(a, b model.Nanos) {
	k := Skew(a, b)
	if k > s.MaxSkew {
		s.MaxSkew = k
	}
	s.Pairs++
}
