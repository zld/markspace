// Package report renders run results as JSON and a terminal summary.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"jitterlab/internal/metrics"
)

// Run identifies a report and the parameters that produced it.
type Run struct {
	Mode        string  `json:"mode"`
	Seed        int64   `json:"seed"`
	DurationMS  int64   `json:"duration_ms"`
	Loss        float64 `json:"loss"`
	Reorder     float64 `json:"reorder"`
	Duplicate   float64 `json:"duplicate"`
	Burst       int     `json:"burst"`
	FrameMS     int     `json:"frame_ms"`
	StreamID    uint16  `json:"stream_id"`
	TargetMinMS float64 `json:"target_min_ms"`
	TargetMaxMS float64 `json:"target_max_ms"`
}

// Report is the top-level JSON document.
type Report struct {
	Run     Run              `json:"run"`
	Summary *metrics.Summary `json:"summary"`
}

// New builds a report and finalises the summary.
func New(run Run, summary *metrics.Summary) *Report {
	if summary == nil {
		summary = metrics.New()
	}
	summary.Finalize()
	return &Report{Run: run, Summary: summary}
}

// WriteJSON writes atomically: the document is encoded to a temporary file in
// the destination directory and renamed over the target only on success.
func (r *Report) WriteJSON(path string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".jitterlab-report-*.json")
	if err != nil {
		return fmt.Errorf("create temp report: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		cleanup()
		return fmt.Errorf("encode report: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync report: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close report: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace report: %w", err)
	}
	return nil
}

// Print writes a stable, key=value terminal summary.
func (r *Report) Print(w io.Writer) {
	s := r.Summary
	fmt.Fprintf(w, "mode=%s\n", r.Run.Mode)
	fmt.Fprintf(w, "packets_received=%d\n", s.PacketsReceived)
	fmt.Fprintf(w, "packets_invalid=%d\n", s.PacketsInvalid)
	fmt.Fprintf(w, "packets_accepted=%d\n", s.PacketsAccepted)
	fmt.Fprintf(w, "drop_duplicate=%d\n", s.DropDuplicate)
	fmt.Fprintf(w, "drop_late=%d\n", s.DropLate)
	fmt.Fprintf(w, "drop_overflow=%d\n", s.DropOverflow)
	fmt.Fprintf(w, "drop_ambiguous=%d\n", s.DropAmbiguous)
	fmt.Fprintf(w, "drop_timestamp_regression=%d\n", s.DropTimestampRegression)
	fmt.Fprintf(w, "stream_interruptions=%d\n", s.StreamInterruptions)
	fmt.Fprintf(w, "frames_played=%d\n", s.FramesPlayed)
	fmt.Fprintf(w, "hidden_frames=%d\n", s.HiddenFrames)
	fmt.Fprintf(w, "max_hidden_run=%d\n", s.MaxHiddenRun)
	fmt.Fprintf(w, "hidden_degradations=%d\n", s.HiddenDegrades)
	fmt.Fprintf(w, "continuity_ratio=%.4f\n", s.ContinuityRatio)
	fmt.Fprintf(w, "latency_p50_ms=%.2f\n", s.LatencyP50MS)
	fmt.Fprintf(w, "latency_p95_ms=%.2f\n", s.LatencyP95MS)
	fmt.Fprintf(w, "buffer_samples=%d\n", s.BufferSamples)
}
