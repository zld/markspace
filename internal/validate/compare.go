package validate

import (
	"fmt"

	"streamcheck/internal/manifest"
)

// CompareRates checks segment counts, id sets, totals and cumulative segment
// boundaries against a reference rate. The reference is the lowest bitrate
// with usable segments; every other rate gets its maximum boundary drift and
// any over-tolerance segment numbers recorded. All findings are appended to
// each rate's Issues.
func CompareRates(checks []RateCheck, toleranceMS int64) {
	refIdx := -1
	for i, c := range checks {
		if len(c.UsableSegments) == 0 {
			continue
		}
		if refIdx < 0 || c.Rate < checks[refIdx].Rate {
			refIdx = i
		}
	}
	if refIdx < 0 {
		return
	}
	ref := checks[refIdx]
	refByID := cumulative(ref.UsableSegments)
	refIDs := make([]int, 0, len(ref.UsableSegments))
	for _, seg := range ref.UsableSegments {
		refIDs = append(refIDs, seg.ID)
	}

	for i := range checks {
		c := &checks[i]
		c.DurationDeltaMS = 0
		c.BoundaryIssues = nil
		if i == refIdx {
			continue
		}
		if c.SegmentCount != ref.SegmentCount {
			c.Issues = append(c.Issues, fmt.Sprintf("segment_count %d; reference rate %d has %d",
				c.SegmentCount, ref.Rate, ref.SegmentCount))
		}
		ids := make([]int, 0, len(c.UsableSegments))
		for _, seg := range c.UsableSegments {
			ids = append(ids, seg.ID)
		}
		if !equalIDs(ids, refIDs) {
			c.Issues = append(c.Issues, fmt.Sprintf("segment_ids [%s] differ from reference rate %d [%s]",
				FormatRanges(ids), ref.Rate, FormatRanges(refIDs)))
		}
		byID := cumulative(c.UsableSegments)
		for _, id := range ids {
			refEnd, ok := refByID[id]
			if !ok {
				continue
			}
			delta := byID[id] - refEnd
			if delta < 0 {
				delta = -delta
			}
			if delta > c.DurationDeltaMS {
				c.DurationDeltaMS = delta
			}
			if delta > toleranceMS {
				c.BoundaryIssues = append(c.BoundaryIssues, BoundaryIssue{SegmentID: id, DeltaMS: byID[id] - refEnd})
				c.Issues = append(c.Issues, fmt.Sprintf("boundary_drift segment %d: %dms exceeds tolerance %dms",
					id, delta, toleranceMS))
			}
		}
	}
}

func cumulative(segments []manifest.Segment) map[int]int64 {
	out := make(map[int]int64, len(segments))
	var total int64
	for _, seg := range segments {
		total += seg.DurationMS
		out[seg.ID] = total
	}
	return out
}

func equalIDs(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
