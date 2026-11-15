package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// WriteText renders the human-readable terminal summary. Every track prints
// the required counters and every failure lists its code, bitrate and seq.
func WriteText(w io.Writer, r *Report) {
	fmt.Fprintf(w, "hlsforge %s\n", r.Command)
	fmt.Fprintln(w, strings.Repeat("-", 60))
	for _, t := range r.Tracks {
		fmt.Fprintf(w, "bitrate_kbps=%d fragments=%d segments=%d parts=%d "+
			"duration_s=%.1f deviation_ms=%d failures=%d\n",
			t.BitrateKbps, t.Fragments, t.Segments, t.Parts,
			t.DurationS, t.DeviationMs, t.Failures)
	}
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "failures: none")
		return
	}
	fmt.Fprintf(w, "failures: %d\n", len(r.Findings))
	for _, f := range r.Findings {
		loc := fmt.Sprintf("bitrate=%d", f.Bitrate)
		if f.Seq > 0 {
			loc += fmt.Sprintf(" seq=%d", f.Seq)
		}
		if f.Part > 0 {
			loc += fmt.Sprintf(" part=%d", f.Part)
		}
		if f.Field != "" {
			loc += " field=" + f.Field
		}
		fmt.Fprintf(w, "  - %s %s: %s\n", f.Code, loc, f.Detail)
	}
}

// WriteJSON renders the machine-readable report.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
