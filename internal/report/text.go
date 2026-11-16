// Package report renders verification results to the terminal as an
// aligned text table or as JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"hlscheck/internal/check"
)

// Summary is the full verification outcome.
type Summary struct {
	Profiles []*check.ProfileReport `json:"profiles"`
	Cross    *check.CrossReport     `json:"cross,omitempty"`
}

// OK reports whether every profile and the cross-check are clean.
func (s *Summary) OK() bool {
	for _, p := range s.Profiles {
		if !p.OK() {
			return false
		}
	}
	return s.Cross == nil || len(s.Cross.Details) == 0
}

// Text writes a per-profile terminal summary.
func Text(w io.Writer, s *Summary) {
	fmt.Fprintln(w, "bitrate_kbps  segments  duration_delta_ms  missing  reordered  checksum_failures")
	for _, p := range s.Profiles {
		fmt.Fprintf(w, "%12d  %8d  %17.1f  %7d  %9d  %17d\n",
			p.BitrateKbps, p.Segments, p.DurationDeltaMs,
			len(p.Missing), p.Reordered, len(p.ChecksumFailures))
	}
	for _, p := range s.Profiles {
		for _, e := range p.Errors {
			fmt.Fprintf(w, "profile %s error: %s\n", p.Name, e)
		}
		for _, m := range p.Missing {
			fmt.Fprintf(w, "profile %s: missing segment seq %d\n", p.Name, m)
		}
		for _, c := range p.ChecksumFailures {
			fmt.Fprintf(w, "profile %s: checksum failure seq %d\n", p.Name, c)
		}
		for _, g := range p.PTSGaps {
			fmt.Fprintf(w, "profile %s: pts %s\n", p.Name, g)
		}
	}
	if s.Cross != nil {
		fmt.Fprintf(w, "cross-profile boundary deviation: %.1f ms\n", s.Cross.BoundaryDeviationMs)
		for _, d := range s.Cross.Details {
			fmt.Fprintln(w, "cross: "+d)
		}
	}
	if s.OK() {
		fmt.Fprintln(w, "RESULT: OK")
	} else {
		fmt.Fprintln(w, "RESULT: INCONSISTENT")
	}
}

// JSON writes the machine-readable summary.
func JSON(w io.Writer, s *Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
