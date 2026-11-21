// Package report renders build results as terminal text or JSON. Reports
// never include file contents, only names, counts, durations and reasons.
package report

import (
	"sort"

	"streamcheck/internal/config"
	"streamcheck/internal/validate"
)

// RateReport is the per-bitrate section of a report.
type RateReport struct {
	RateKbps             int      `json:"rate_kbps"`
	SegmentCount         int      `json:"segment_count"`
	DiscoveredCount      int      `json:"discovered_file_count"`
	TotalDurationSeconds float64  `json:"total_duration_seconds"`
	DurationDeltaMS      int64    `json:"duration_delta_ms"`
	MissingRanges        []string `json:"missing_ranges"`
	DuplicateIDs         []int    `json:"duplicate_ids"`
	ValidationFailures   []string `json:"validation_failures"`
	ManifestPath         string   `json:"manifest_path"`
	ManifestSHA256       string   `json:"manifest_sha256"`
}

// Report is the top-level document. Per-rate fields are the primary output;
// the top-level roll-ups make the JSON usable without iterating.
type Report struct {
	Input             string       `json:"input"`
	Pattern           string       `json:"pattern"`
	SegmentSeconds    float64      `json:"segment_seconds"`
	ToleranceMS       int64        `json:"tolerance_ms"`
	Strict            bool         `json:"strict"`
	Rates             []RateReport `json:"rates"`
	Warnings          []string     `json:"warnings"`
	GlobalFailures    []string     `json:"validation_failures"`
	DuplicateIDs      []int        `json:"duplicate_ids"`
	MissingRanges     []string     `json:"missing_ranges"`
	SegmentCount      int          `json:"segment_count"`
	TotalDurationSecs float64      `json:"total_duration_seconds"`
	DurationDeltaMS   int64        `json:"duration_delta_ms"`
	Failed            bool         `json:"failed"`
	ExitCode          int          `json:"exit_code"`
}

// Build assembles a Report from per-rate checks and global issues.
func Build(cfg *config.Build, checks []validate.RateCheck, globalIssues, warnings []string, digests map[int]string) Report {
	r := Report{
		Input:          cfg.Input,
		Pattern:        cfg.Pattern,
		SegmentSeconds: cfg.SegmentSeconds,
		ToleranceMS:    cfg.ToleranceMS,
		Strict:         cfg.Strict,
		GlobalFailures: append([]string{}, globalIssues...),
		Warnings:       append([]string{}, warnings...),
		DuplicateIDs:   []int{},
		MissingRanges:  []string{},
	}
	dupSet := map[int]bool{}
	for _, c := range checks {
		secs := float64(c.TotalMS) / 1000
		r.Rates = append(r.Rates, RateReport{
			RateKbps:             c.Rate,
			SegmentCount:         c.SegmentCount,
			DiscoveredCount:      c.DiscoveredCount,
			TotalDurationSeconds: roundMillis(secs),
			DurationDeltaMS:      c.DurationDeltaMS,
			MissingRanges:        ranges(c.MissingIDs),
			DuplicateIDs:         append([]int{}, c.DuplicateIDs...),
			ValidationFailures:   nonNilStrings(c.Issues),
			ManifestPath:         c.ManifestPath,
			ManifestSHA256:       digests[c.Rate],
		})
		r.SegmentCount += c.SegmentCount
		r.TotalDurationSecs += secs
		if c.DurationDeltaMS > r.DurationDeltaMS {
			r.DurationDeltaMS = c.DurationDeltaMS
		}
		r.MissingRanges = append(r.MissingRanges, ranges(c.MissingIDs)...)
		for _, id := range c.DuplicateIDs {
			dupSet[id] = true
		}
	}
	sort.Slice(r.Rates, func(i, j int) bool { return r.Rates[i].RateKbps < r.Rates[j].RateKbps })
	r.TotalDurationSecs = roundMillis(r.TotalDurationSecs)
	r.DuplicateIDs = sortedKeys(dupSet)
	sort.Strings(r.MissingRanges)
	r.Failed = len(r.GlobalFailures) > 0
	for _, rr := range r.Rates {
		if len(rr.ValidationFailures) > 0 {
			r.Failed = true
		}
	}
	r.MissingRanges = dedupe(r.MissingRanges)
	if r.Failed {
		r.ExitCode = 1
	}
	return r
}

func ranges(ids []int) []string {
	if len(ids) == 0 {
		return []string{}
	}
	return []string{validate.FormatRanges(ids)}
}

func roundMillis(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func sortedKeys(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
