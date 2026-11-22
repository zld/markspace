package segment

import (
	"fmt"

	"mediq/internal/timeline"
)

// Alignment is the cross-representation result: every representation
// shares the same boundary count and each boundary's target start time.
type Alignment struct {
	Boundaries int
	Targets    []timeline.Ticks // target StartPTS per boundary index
	Plans      []*Plan
	SkewMaxMs  float64 // largest observed boundary skew in milliseconds
}

// AlignConfig carries the cross-representation tolerance.
type AlignConfig struct {
	Tolerance timeline.Ticks
}

// DefaultAlignConfig allows up to 40 ms boundary skew.
func DefaultAlignConfig() AlignConfig {
	return AlignConfig{Tolerance: timeline.MustMs(40)}
}

// Align verifies that every plan has the same boundary numbering and
// that matching boundaries start within tolerance on the unified
// clock. Quarantined segments keep their boundary number but are
// excluded from skew measurement (damage is reported separately).
// On success each clean segment is stamped with the shared target
// time and SourceAligned; exceeding tolerance rejects the whole group.
func Align(plans []*Plan, cfg AlignConfig) (*Alignment, error) {
	if len(plans) == 0 {
		return nil, timeline.New(timeline.StageAlign, timeline.KindInconsistent, -1,
			"abort_align", "no representations to align")
	}
	boundaries := len(plans[0].Segments)
	for _, p := range plans[1:] {
		if len(p.Segments) != boundaries {
			return nil, timeline.New(timeline.StageAlign, timeline.KindSkewExceeded, -1,
				"reject_group",
				fmt.Sprintf("rep %s has %d boundaries, want %d", p.RepID, len(p.Segments), boundaries))
		}
	}
	a := &Alignment{Boundaries: boundaries, Plans: plans}
	for b := 0; b < boundaries; b++ {
		var minTS, maxTS timeline.Ticks
		first := true
		for _, p := range plans {
			s := p.Segments[b]
			if s.Index != b {
				return nil, timeline.New(timeline.StageAlign, timeline.KindInconsistent,
					s.StartOffset, "reject_group",
					fmt.Sprintf("rep %s segment numbering gap at boundary %d", p.RepID, b))
			}
			if s.Quarantined {
				continue
			}
			if first || s.StartPTS < minTS {
				minTS = s.StartPTS
			}
			if first || s.StartPTS > maxTS {
				maxTS = s.StartPTS
			}
			first = false
		}
		if first {
			// All representations quarantined at this boundary: keep
			// the first plan's time as the nominal target.
			minTS = plans[0].Segments[b].StartPTS
			maxTS = minTS
		}
		skew := timeline.AbsDiff(maxTS, minTS)
		if skew > cfg.Tolerance {
			return nil, timeline.New(timeline.StageAlign, timeline.KindSkewExceeded,
				plans[0].Segments[b].StartOffset, "reject_group",
				fmt.Sprintf("boundary %d skew %.2fms exceeds tolerance %.2fms",
					b, skew.MillisFloat(), cfg.Tolerance.MillisFloat()))
		}
		if ms := skew.MillisFloat(); ms > a.SkewMaxMs {
			a.SkewMaxMs = ms
		}
		a.Targets = append(a.Targets, minTS)
		for _, p := range plans {
			if !p.Segments[b].Quarantined {
				p.Segments[b].IndexSource = SourceAligned
			}
		}
	}
	return a, nil
}
