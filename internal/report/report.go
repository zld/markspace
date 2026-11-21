// Package report renders verification results to the terminal as readable
// text and as machine-parseable JSON, using only the standard library.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"streamcheck/internal/verify"
)

// Report is the top-level serialisable result of a demo or verify run.
type Report struct {
	Tiers              []verify.TierResult `json:"tiers"`
	ValidationFailures int                 `json:"validation_failures"`
	MaxDeviationMS     int64               `json:"max_duration_deviation_ms"`
}

// FromResults assembles a Report from per-tier verification results.
func FromResults(results []verify.TierResult) Report {
	tiers := make([]verify.TierResult, len(results))
	copy(tiers, results)
	for i := range tiers {
		// Emit an empty array rather than null when there are no failures.
		if tiers[i].Failures == nil {
			tiers[i].Failures = []verify.Failure{}
		}
	}
	r := Report{Tiers: tiers}
	for _, t := range tiers {
		r.ValidationFailures += len(t.Failures)
		if t.DeviationMS > r.MaxDeviationMS {
			r.MaxDeviationMS = t.DeviationMS
		}
	}
	return r
}

// JSON writes a stable, indented JSON representation.
func (r Report) JSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Text writes a human-readable terminal summary followed by failure detail.
func (r Report) Text(w io.Writer) {
	fmt.Fprintln(w, "streamcheck report")
	fmt.Fprintln(w, strings.Repeat("-", 60))
	fmt.Fprintf(w, "%-8s %-12s %-9s %-18s %-9s\n",
		"tier", "bitrate_bps", "segments", "duration_dev_ms", "failures")
	for _, t := range r.Tiers {
		fmt.Fprintf(w, "%-8s %-12d %-9d %-18d %-9d\n",
			t.Tier, t.BitrateBPS, t.Segments, t.DeviationMS, len(t.Failures))
	}
	fmt.Fprintln(w, strings.Repeat("-", 60))
	fmt.Fprintf(w, "max_duration_deviation_ms=%d validation_failures=%d\n",
		r.MaxDeviationMS, r.ValidationFailures)
	if r.ValidationFailures == 0 {
		fmt.Fprintln(w, "result: PASS")
		return
	}
	fmt.Fprintln(w, "result: FAIL")
	for _, t := range r.Tiers {
		for _, f := range t.Failures {
			loc := f.Segment
			if loc == "" {
				loc = "-"
			}
			fmt.Fprintf(w, "  [%s] tier=%s segment=%s seq=%d %s\n",
				f.Code, f.Tier, loc, f.Sequence, f.Message)
		}
	}
}
