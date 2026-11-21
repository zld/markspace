// Package validate cross-checks discovered segments, generated playlists and
// the files on disk. It reuses manifest.Parse and manifest rules so that
// verification follows exactly the same rules as generation.
package validate

import (
	"fmt"
	"sort"
	"strings"

	"streamcheck/internal/manifest"
)

// ProbeFailure records one segment whose duration could not be read.
type ProbeFailure struct {
	ID       int    `json:"id"`
	FileName string `json:"file_name"`
	Reason   string `json:"reason"`
}

// BoundaryIssue records a cumulative-duration drift at one segment number.
type BoundaryIssue struct {
	SegmentID int   `json:"segment_id"`
	DeltaMS   int64 `json:"delta_ms"`
}

// RateCheck is the full consistency picture for one bitrate.
type RateCheck struct {
	Rate int
	// DiscoveredCount counts files on disk for this rate, including
	// duplicates and files whose probe failed.
	DiscoveredCount int
	SegmentCount    int
	TotalMS         int64
	MissingIDs      []int
	DuplicateIDs    []int
	DuplicatePaths  map[int][]string
	ProbeFailures   []ProbeFailure
	Issues          []string
	ManifestPath    string
	DurationDeltaMS int64
	BoundaryIssues  []BoundaryIssue
	UsableSegments  []manifest.Segment
}

// AnalyzeRate reports missing ids, duplicates and probe failures for one
// bitrate. Missing ids are fatal only in strict mode; duplicates and probe
// failures can never be emitted and are always failures.
func AnalyzeRate(rate int, segments []manifest.Segment, duplicates map[int][]string, strict bool) RateCheck {
	c := RateCheck{Rate: rate, DuplicatePaths: duplicates}
	present := map[int]bool{}
	for _, seg := range segments {
		if seg.RateKbps != rate {
			continue
		}
		c.DiscoveredCount++
		// Every discovered file counts as present, even when its probe
		// failed: gap detection is about files, not about usable durations.
		present[seg.ID] = true
		if seg.Usable() {
			c.UsableSegments = append(c.UsableSegments, seg)
			c.TotalMS += seg.DurationMS
			continue
		}
		reason := seg.ProbeError
		if reason == "" {
			reason = "zero duration"
		}
		c.ProbeFailures = append(c.ProbeFailures, ProbeFailure{ID: seg.ID, FileName: seg.FileName, Reason: reason})
		c.Issues = append(c.Issues, fmt.Sprintf("probe_failed %s: %s", seg.FileName, reason))
	}
	sort.Slice(c.UsableSegments, func(i, j int) bool { return c.UsableSegments[i].ID < c.UsableSegments[j].ID })
	c.SegmentCount = len(c.UsableSegments)

	for id := range duplicates {
		c.DuplicateIDs = append(c.DuplicateIDs, id)
		c.DiscoveredCount += len(duplicates[id])
		present[id] = true
	}
	sort.Ints(c.DuplicateIDs)
	for _, id := range c.DuplicateIDs {
		names := append([]string(nil), duplicates[id]...)
		sort.Strings(names)
		c.Issues = append(c.Issues, fmt.Sprintf("duplicate_segment_id %d: %s", id, strings.Join(names, ", ")))
	}

	if len(present) > 0 {
		minID, maxID := -1, -1
		for id := range present {
			if minID < 0 || id < minID {
				minID = id
			}
			if id > maxID {
				maxID = id
			}
		}
		for id := minID; id <= maxID; id++ {
			if !present[id] {
				c.MissingIDs = append(c.MissingIDs, id)
			}
		}
	}
	if strict && len(c.MissingIDs) > 0 {
		c.Issues = append(c.Issues, "missing_segment_ids "+FormatRanges(c.MissingIDs))
	}
	return c
}

// FormatRanges collapses sorted ids into "3-4,7" form.
func FormatRanges(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	var parts []string
	start, prev := ids[0], ids[0]
	for _, id := range ids[1:] {
		if id == prev+1 {
			prev = id
			continue
		}
		parts = append(parts, formatRange(start, prev))
		start, prev = id, id
	}
	parts = append(parts, formatRange(start, prev))
	return strings.Join(parts, ",")
}

func formatRange(start, end int) string {
	if start == end {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}
