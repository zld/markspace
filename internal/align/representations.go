// Package align aligns the fragment boundaries of multiple bitrate
// representations on the shared nanosecond timeline.
//
// Each representation supplies its own track timescale; the clock package
// converts every tfdt to nanoseconds. A boundary window clusters fragment
// starts whose timestamps differ by at most the tolerance, which the spec
// fixes to one tick of the coarsest (lowest-rate) timescale. Windows that do
// not contain every representation are reported as misaligned; the manifest
// layer treats any misalignment as a validation failure.
package align

import (
	"sort"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/scan"
)

// Input is one representation's sequence analysis plus its time base.
type Input struct {
	Representation string
	Timescale      uint32
	Report         *scan.TrackReport
}

// Boundary is one aligned segment boundary on the shared timeline.
type Boundary struct {
	// Index is the zero-based aligned segment index.
	Index int `json:"index"`
	// StartNanos is the window's reference start time (minimum member).
	StartNanos int64 `json:"start_nanos"`
	// EndNanos is the window's reference end time (minimum member end).
	EndNanos int64 `json:"end_nanos"`
	// Members maps representation ID to its fragment at this boundary.
	Members map[string]*Member `json:"members"`
	// Aligned reports whether every representation joined the window inside
	// the configured tolerance.
	Aligned bool `json:"aligned"`
}

// Member is one representation's contribution to a boundary.
type Member struct {
	MFHD       uint32 `json:"mfhd"`
	TFDT       uint64 `json:"tfdt"`
	StartNanos int64  `json:"start_nanos"`
	EndNanos   int64  `json:"end_nanos"`
	SHA256     string `json:"sha256"`
	InputPath  string `json:"input_path"`
}

// Report is the alignment result for a fragment group.
type Report struct {
	RepresentationCount int        `json:"representation_count"`
	ToleranceNanos      int64      `json:"tolerance_nanos"`
	Boundaries          []Boundary `json:"boundaries"`
	// Misaligned indexes (a subset of Boundaries indexes).
	Misaligned []int `json:"misaligned_indexes"`
	// Regressions are surfaced from the scan reports for manifest trace.
	Regressions []scan.Regression `json:"regressions"`
}

// AlignedSegmentCount returns the number of boundaries covering every
// representation within tolerance.
func (r *Report) AlignedSegmentCount() int {
	n := 0
	for i := range r.Boundaries {
		if r.Boundaries[i].Aligned {
			n++
		}
	}
	return n
}

// timedFragment pairs a scan fragment with its normalized nanosecond range.
type timedFragment struct {
	repID      string
	frag       *scan.Fragment
	startNanos int64
	endNanos   int64
}

// Align performs multi-representation boundary alignment.
func Align(inputs []Input) (*Report, error) {
	if len(inputs) < 2 {
		return nil, errNeedTwoRepresentations()
	}
	timelines := make(map[string]*clock.Timeline, len(inputs))
	var coarseTick int64 = -1
	for _, in := range inputs {
		tl, err := clock.NewTimeline(in.Timescale)
		if err != nil {
			return nil, err
		}
		timelines[in.Representation] = tl
		// The lowest-rate timescale has the longest tick; one such tick is
		// the spec's alignment tolerance.
		if tick := tl.TickNanos(); coarseTick < 0 || tick > coarseTick {
			coarseTick = tick
		}
	}

	// Flatten accepted fragments (isolated regressions are excluded by scan)
	// into one global, time-sorted list.
	var all []timedFragment
	regressions := []scan.Regression{}
	for _, in := range inputs {
		tl := timelines[in.Representation]
		for _, f := range in.Report.Fragments {
			start := tl.ToNanos(f.TFDT)
			end := tl.ToNanos(f.TFDT + in.Report.FragmentDuration(f.MFHD))
			all = append(all, timedFragment{
				repID:      in.Representation,
				frag:       f,
				startNanos: start,
				endNanos:   end,
			})
		}
		regressions = append(regressions, in.Report.Regressions...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].startNanos != all[j].startNanos {
			return all[i].startNanos < all[j].startNanos
		}
		return all[i].repID < all[j].repID
	})

	report := &Report{
		RepresentationCount: len(inputs),
		ToleranceNanos:      coarseTick,
		Regressions:         regressions,
	}
	repSet := map[string]bool{}
	for _, in := range inputs {
		repSet[in.Representation] = true
	}

	// Greedy window clustering: start a window at the earliest unassigned
	// fragment and absorb every fragment within tolerance of the window
	// anchor. A representation appears at most once per window.
	used := make([]bool, len(all))
	for {
		anchor := -1
		for i, u := range used {
			if !u {
				anchor = i
				break
			}
		}
		if anchor == -1 {
			break
		}
		b := Boundary{
			Index:      len(report.Boundaries),
			StartNanos: all[anchor].startNanos,
			EndNanos:   all[anchor].endNanos,
			Members:    map[string]*Member{},
		}
		minEnd := all[anchor].endNanos
		for i := anchor; i < len(all); i++ {
			if used[i] {
				continue
			}
			tf := all[i]
			if !clock.WithinTolerance(tf.startNanos, b.StartNanos, coarseTick) {
				continue
			}
			if _, exists := b.Members[tf.repID]; exists {
				continue // representation already represented in this window
			}
			b.Members[tf.repID] = &Member{
				MFHD:       tf.frag.MFHD,
				TFDT:       tf.frag.TFDT,
				StartNanos: tf.startNanos,
				EndNanos:   tf.endNanos,
				SHA256:     tf.frag.SHA256,
				InputPath:  tf.frag.InputPath,
			}
			used[i] = true
			if tf.endNanos < minEnd {
				minEnd = tf.endNanos
			}
		}
		b.EndNanos = minEnd
		b.Aligned = len(b.Members) == len(inputs)
		if !b.Aligned {
			report.Misaligned = append(report.Misaligned, b.Index)
		}
		report.Boundaries = append(report.Boundaries, b)
	}
	return report, nil
}
