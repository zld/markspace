// Package fanin merges the representation layers back onto the shared clock
// and validates switch boundaries. This file declares the merger's
// configuration and result types.
package fanin

import (
	"mediad/internal/media"
	"mediad/internal/merr"
)

// DefaultToleranceNs is the maximum accepted boundary deviation (20 ms).
const DefaultToleranceNs int64 = 20_000_000

// Switch asks the merger to move a track onto a representation at a given
// shared-clock instant.
type Switch struct {
	Track media.TrackID
	AtNs  int64
	ToRep string
}

// Options tunes the merger.
type Options struct {
	// ToleranceNs is the accepted boundary deviation. Default 20 ms.
	ToleranceNs int64
	// MaxGapNs is the largest acceptable hole between consecutive merged
	// blocks on one track. Default 2 s.
	MaxGapNs int64
	// Plan lists representation switches. Without a plan each track stays on
	// the first representation carrying it.
	Plan []Switch
}

func (o Options) withDefaults() Options {
	if o.ToleranceNs <= 0 {
		o.ToleranceNs = DefaultToleranceNs
	}
	if o.MaxGapNs <= 0 {
		o.MaxGapNs = 2_000_000_000
	}
	return o
}

// Boundary is one accepted representation switch.
type Boundary struct {
	Track       media.TrackID
	TimeNs      int64
	FromRep     string
	ToRep       string
	FromTime    int64
	ToTime      int64
	DeviationNs int64
}

// Mismatch explains a rejected boundary.
type Mismatch struct {
	Track       media.TrackID
	FromRep     string
	ToRep       string
	TimeNs      int64
	DeviationNs int64
	// Reason is "deviation" when the two sides disagree, or "missing" when
	// one side has no block near the switch.
	Reason string
}

// String renders a mismatch for the report.
func (m Mismatch) String() string {
	return merr.New("fanin.boundary", merr.CodeBoundaryMismatch, -1,
		"track %d switch %s->%s at %d ms rejected (%s, deviation %.2f ms)",
		m.Track, m.FromRep, m.ToRep, m.TimeNs/1e6, m.Reason, float64(m.DeviationNs)/1e6).Error()
}

// Result is a merged timeline.
type Result struct {
	Blocks     []media.Block
	Boundaries []Boundary
	Mismatches []Mismatch
	// Missing counts blocks the planned representation did not carry.
	Missing int
	// SwitchCount counts attempted switches.
	SwitchCount int
	// MaxBoundaryDeviationNs is the worst accepted deviation.
	MaxBoundaryDeviationNs int64
	// Dropouts counts intervals where a track skipped at least one nominal
	// cadence, i.e. transport loss that survived recovery and reached the
	// merger. It is the fan-in view of upstream packet loss.
	Dropouts int
	// MaxDropoutMs is the worst post-recovery gap on any track.
	MaxDropoutMs float64
}
