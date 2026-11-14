// Package report renders the run report as JSON and as a terminal summary.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"transcode/internal/media"
)

// StatsPrecision is the number of decimal places kept for dB and bitrate
// figures in both JSON and terminal output.
const StatsPrecision = 3

// SegmentReport captures one segment's measurements. When Valid is false no
// quality numbers are fabricated: PSNR fields stay zero and InvalidReason
// explains why.
type SegmentReport struct {
	Index                int      `json:"index"`
	StartSec             float64  `json:"start_sec"`
	DurationSec          float64  `json:"duration_sec"`
	Valid                bool     `json:"valid"`
	InvalidReason        string   `json:"invalid_reason,omitempty"`
	Frames               int      `json:"frames"`
	ExpectedFrames       int      `json:"expected_frames"`
	PSNRMeanDB           float64  `json:"psnr_mean_db,omitempty"`
	PSNRMinDB            float64  `json:"psnr_min_db,omitempty"`
	InfFrames            int      `json:"inf_frames"`
	ActualKbps           float64  `json:"actual_kbps"`
	SizeBytes            int64    `json:"size_bytes"`
	ContainerOverheadPct float64  `json:"container_overhead_pct"`
	FFmpegArgs           []string `json:"ffmpeg_args"`
}

// JobReport captures one job's outcome.
type JobReport struct {
	ID             string           `json:"id"`
	Input          string           `json:"input"`
	OutputDir      string           `json:"output_dir,omitempty"`
	State          string           `json:"state"`
	Error          string           `json:"error,omitempty"`
	InputSummary   media.ProbeInfo  `json:"input_summary"`
	ActualKbps     float64          `json:"actual_kbps"`
	DeviationPct   float64          `json:"deviation_pct"`
	PSNRMeanDB     float64          `json:"psnr_mean_db,omitempty"`
	PSNRMinDB      float64          `json:"psnr_min_db,omitempty"`
	Frames         int              `json:"frames"`
	Segments       []SegmentReport  `json:"segments,omitempty"`
}

// Report is the top-level run document written to <out>/report.json.
type Report struct {
	GeneratedAt      string            `json:"generated_at"`
	FFmpegVersion    string            `json:"ffmpeg_version"`
	Digest           string            `json:"digest"`
	Policy           string            `json:"policy"`
	StatsPrecision   int               `json:"stats_precision"`
	PSNRSaturationDB float64           `json:"psnr_saturation_db"`
	Config           map[string]string `json:"config"`
	TargetKbps       int               `json:"target_kbps"`
	ActualKbps       float64           `json:"actual_kbps"`
	DeviationPct     float64           `json:"deviation_pct"`
	JobsTotal        int               `json:"jobs_total"`
	JobsSuccess      int               `json:"jobs_success"`
	JobsFailed       int               `json:"jobs_failed"`
	JobsCanceled     int               `json:"jobs_canceled"`
	Jobs             []JobReport       `json:"jobs"`
}

// WriteJSON atomically writes the report (temp file + rename).
func WriteJSON(path string, r *Report) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("report: encode: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("report: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("report: commit: %w", err)
	}
	return nil
}

// WriteJobJSON writes a per-job report inside dir (non-atomic; the directory
// rename that follows provides atomicity).
func WriteJobJSON(dir string, jr *JobReport) error {
	b, err := json.MarshalIndent(jr, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644)
}

// ReadJobJSON loads a previously committed per-job report (used on resume).
func ReadJobJSON(dir string) (*JobReport, error) {
	b, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		return nil, err
	}
	var jr JobReport
	if err := json.Unmarshal(b, &jr); err != nil {
		return nil, err
	}
	return &jr, nil
}

// PrintSummary writes the terminal summary in the documented key=value form.
func PrintSummary(w io.Writer, r *Report) {
	fmt.Fprintf(w, "ffmpeg=%q\n", r.FFmpegVersion)
	fmt.Fprintf(w, "digest=%s\n", r.Digest)
	fmt.Fprintf(w, "policy=%s\n", r.Policy)
	for _, j := range r.Jobs {
		if j.State != "success" {
			fmt.Fprintf(w, "job=%s state=%s error=%q\n", j.ID, j.State, j.Error)
			continue
		}
		fmt.Fprintf(w, "job=%s state=%s actual_kbps=%.3f deviation_pct=%.3f psnr_mean_db=%.3f frames=%d\n",
			j.ID, j.State, j.ActualKbps, j.DeviationPct, j.PSNRMeanDB, j.Frames)
		for _, s := range j.Segments {
			if !s.Valid {
				fmt.Fprintf(w, "job=%s segment=%02d valid=false reason=%q\n", j.ID, s.Index, s.InvalidReason)
				continue
			}
			fmt.Fprintf(w, "job=%s segment=%02d psnr_mean_db=%.3f psnr_min_db=%.3f frames=%d actual_kbps=%.3f overhead_pct=%.3f\n",
				j.ID, s.Index, s.PSNRMeanDB, s.PSNRMinDB, s.Frames, s.ActualKbps, s.ContainerOverheadPct)
		}
	}
	fmt.Fprintf(w, "jobs=%d success=%d failed=%d canceled=%d\n",
		r.JobsTotal, r.JobsSuccess, r.JobsFailed, r.JobsCanceled)
	fmt.Fprintf(w, "target_kbps=%d actual_kbps=%.3f deviation_pct=%.3f\n",
		r.TargetKbps, r.ActualKbps, r.DeviationPct)
}
