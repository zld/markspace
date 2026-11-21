package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func ints(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ",")
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// WriteJSON writes the report as indented JSON with a trailing newline.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes a human-readable summary.
func WriteText(w io.Writer, r Report) {
	fmt.Fprintf(w, "streamcheck: %s (pattern %s)\n", r.Input, r.Pattern)
	for _, rr := range r.Rates {
		fmt.Fprintf(w, "\nrate %d kbps\n", rr.RateKbps)
		fmt.Fprintf(w, "  segments          : %d\n", rr.SegmentCount)
		fmt.Fprintf(w, "  total duration    : %.3fs\n", rr.TotalDurationSeconds)
		fmt.Fprintf(w, "  boundary delta    : %dms (tolerance %dms)\n", rr.DurationDeltaMS, r.ToleranceMS)
		fmt.Fprintf(w, "  missing ranges    : %s\n", orNone(strings.Join(rr.MissingRanges, ",")))
		fmt.Fprintf(w, "  duplicate ids     : %s\n", orNone(ints(rr.DuplicateIDs)))
		fmt.Fprintf(w, "  manifest          : %s\n", orNone(rr.ManifestPath))
		fmt.Fprintf(w, "  manifest sha256   : %s\n", orNone(rr.ManifestSHA256))
		fmt.Fprintf(w, "  validation errors : %d\n", len(rr.ValidationFailures))
		for _, issue := range rr.ValidationFailures {
			fmt.Fprintf(w, "    - %s\n", issue)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintf(w, "\nwarnings (%d)\n", len(r.Warnings))
		for _, warning := range r.Warnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
	}
	if len(r.GlobalFailures) > 0 {
		fmt.Fprintf(w, "\nglobal failures (%d)\n", len(r.GlobalFailures))
		for _, issue := range r.GlobalFailures {
			fmt.Fprintf(w, "  - %s\n", issue)
		}
	}
	status := "PASS"
	if r.Failed {
		status = "FAIL"
	}
	fmt.Fprintf(w, "\nresult: %s (exit %d)\n", status, r.ExitCode)
}
