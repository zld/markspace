package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"hlsflow/internal/pipeline"
)

// filepathDir returns the containing directory of a playlist path.
func filepathDir(p string) string { return filepath.Dir(p) }

// renderText prints one terminal line per rendition in the acceptance format.
func renderText(report *pipeline.Report) {
	for _, r := range report.Renditions {
		sign := "+"
		if r.DriftSeconds < 0 {
			sign = ""
		}
		fmt.Printf("rendition=%s bitrate=%d segments=%d duration=%.2fs drift=%s%.2fs missing=%d reordered=%d checksum_failures=%d\n",
			r.Rendition, r.Bitrate, r.Segments, r.DurationSeconds,
			sign, r.DriftSeconds, r.Missing, r.Reordered, r.ChecksumFailures)
		for _, f := range r.Failures {
			fmt.Printf("  FAIL rendition=%s segment=%d reason=%q\n",
				f.Rendition, f.Segment, f.Reason)
		}
	}
	for _, extra := range report.ExtraFiles {
		fmt.Printf("  EXTRA file=%s\n", extra)
	}
}

// renderJSON emits the full report, including per-segment failure reasons.
func renderJSON(report *pipeline.Report) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
