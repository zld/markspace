// Package report renders per-cue and summary results to the terminal.
package report

import (
	"fmt"
	"io"
	"strings"
)

// CueLine is the per-cue verification row.
type CueLine struct {
	Cue              int
	StartFrame       int64
	EndFrame         int64
	RoundtripDeltaMS int64
	Dropped          bool
}

// FileResult aggregates one input file's conversion and verification.
type FileResult struct {
	Path           string
	Encoding       string
	InputCount     int
	OutputCount    int
	Dropped        int
	MaxAbsDeltaMS  int64
	Cues           []CueLine
	Error          string
}

// Summary is the batch-level rollup.
type Summary struct {
	Files        int
	Failed       int
	TotalInput   int
	TotalOutput  int
	TotalDropped int
	MaxAbsDelta  int64
}

// File writes one file's report. Paths sort upstream; this function only
// renders what it is given.
func File(w io.Writer, r FileResult) {
	fmt.Fprintf(w, "file=%s encoding=%s\n", r.Path, r.Encoding)
	if r.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", r.Error)
		return
	}
	for _, c := range r.Cues {
		fmt.Fprintf(w, "  cue=%d start_frame=%d end_frame=%d roundtrip_delta_ms=%d dropped=%t\n",
			c.Cue, c.StartFrame, c.EndFrame, c.RoundtripDeltaMS, c.Dropped)
	}
	fmt.Fprintf(w, "  summary: input=%d output=%d dropped=%d max_abs_delta_ms=%d encoding=%s\n",
		r.InputCount, r.OutputCount, r.Dropped, r.MaxAbsDeltaMS, r.Encoding)
}

// Batch writes the sorted per-file reports followed by a rollup line.
func Batch(w io.Writer, results []FileResult) Summary {
	s := Summary{Files: len(results)}
	for _, r := range results {
		File(w, r)
		if r.Error != "" {
			s.Failed++
			continue
		}
		s.TotalInput += r.InputCount
		s.TotalOutput += r.OutputCount
		s.TotalDropped += r.Dropped
		if r.MaxAbsDeltaMS > s.MaxAbsDelta {
			s.MaxAbsDelta = r.MaxAbsDeltaMS
		}
	}
	if s.Files > 1 {
		fmt.Fprintf(w, "batch: files=%d failed=%d input=%d output=%d dropped=%d max_abs_delta_ms=%d\n",
			s.Files, s.Failed, s.TotalInput, s.TotalOutput, s.TotalDropped, s.MaxAbsDelta)
	}
	return s
}

// AbsInt64 returns the absolute value without relying on math.Abs float math.
func AbsInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Join is a small helper kept here so callers render error lists uniformly.
func Join(errs []string) string { return strings.Join(errs, "; ") }
