package clock

import (
	"mediarc/internal/model"
)

// FanInConfig controls shared-timeline cross-checks.
type FanInConfig struct {
	// BoundaryThresholdMs is the maximum tolerated boundary start difference
	// between representations on the shared millisecond timeline.
	BoundaryThresholdMs int64
}

// FanInResult is the shared-clock output.
type FanInResult struct {
	// ReferenceID is the representation that anchors the common timeline.
	ReferenceID string
	// CommonBoundaries are the reference boundaries in shared milliseconds.
	CommonBoundaries []model.Boundary
	// Anomalies lists every cross-representation boundary mismatch.
	Anomalies []model.AnomalyInterval
	// Untrusted lists representations whose boundaries could not participate.
	Untrusted []string
}

type boundaryPoint struct {
	repID    string
	index    int
	startMs  int64
	endMs    int64
}

// FanIn merges independently recovered representations onto one shared time
// basis and cross-checks segment boundaries. Representations flagged untrusted
// during recovery never participate in comparisons but are reported instead of
// being silently dropped.
func FanIn(reps []model.RecoveredRepresentation, cfg FanInConfig) FanInResult {
	res := FanInResult{}
	// Group boundary points by segment index across trusted representations.
	byIndex := map[int][]boundaryPoint{}
	maxIndex := -1
	for _, rep := range reps {
		trusted := false
		for _, tr := range rep.Tracks {
			if tr.Trusted {
				trusted = true
				break
			}
		}
		if !trusted {
			res.Untrusted = append(res.Untrusted, rep.ID)
			continue
		}
		if res.ReferenceID == "" {
			res.ReferenceID = rep.ID
		}
		// Use the first trusted track's boundaries as the representation view.
		var bounds []model.Boundary
		for _, tr := range rep.Tracks {
			if tr.Trusted {
				bounds = tr.Boundaries
				break
			}
		}
		for _, b := range bounds {
			byIndex[b.SegmentIndex] = append(byIndex[b.SegmentIndex], boundaryPoint{
				repID: rep.ID, index: b.SegmentIndex, startMs: b.StartMs, endMs: b.EndMs,
			})
			if b.SegmentIndex > maxIndex {
				maxIndex = b.SegmentIndex
			}
		}
	}

	for idx := 0; idx <= maxIndex; idx++ {
		points := byIndex[idx]
		if len(points) < 2 {
			continue
		}
		minStart, maxStart := points[0].startMs, points[0].startMs
		maxEnd := points[0].endMs
		participants := make([]string, 0, len(points))
		for _, p := range points {
			participants = append(participants, p.repID)
			if p.startMs < minStart {
				minStart = p.startMs
			}
			if p.startMs > maxStart {
				maxStart = p.startMs
			}
			if p.endMs > maxEnd {
				maxEnd = p.endMs
			}
		}
		delta := maxStart - minStart
		if delta > cfg.BoundaryThresholdMs {
			res.Anomalies = append(res.Anomalies, model.AnomalyInterval{
				StartMs:      minStart,
				EndMs:        maxEnd,
				ThresholdMs:  cfg.BoundaryThresholdMs,
				MaxDeltaMs:   delta,
				Participants: participants,
				Kind:         model.KindBoundaryMismatch,
				Detail:       "segment " + itoa(idx) + " boundary starts diverge by " + itoa64(delta) + " ms",
			})
		}
	}

	// The common timeline is the reference representation's boundary set.
	for _, rep := range reps {
		if rep.ID != res.ReferenceID {
			continue
		}
		for _, tr := range rep.Tracks {
			if tr.Trusted {
				res.CommonBoundaries = append([]model.Boundary(nil), tr.Boundaries...)
				break
			}
		}
	}
	return res
}
