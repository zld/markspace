// Package report aggregates the final state into a JSON document and
// a terminal summary. Every number comes from measured segment
// results; nothing is estimated.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"transcode/internal/job"
)

// SegmentReport mirrors one segment's measured outcome.
type SegmentReport struct {
	Index      int     `json:"index"`
	Status     string  `json:"status"`
	Attempts   int     `json:"attempts"`
	TargetKbps float64 `json:"target_kbps"`
	ActualKbps float64 `json:"actual_kbps"`
	PSNRMeanDB float64 `json:"psnr_mean_db"`
	PSNRMinDB  float64 `json:"psnr_min_db"`
	MaxSwingDB float64 `json:"max_swing_db"`
	BelowFloor int     `json:"below_floor_frames"`
	Frames     int     `json:"frames"`
	Error      string  `json:"error,omitempty"`
}

// Report is the top-level run summary written to the report path.
type Report struct {
	Input          string          `json:"input"`
	TargetKbps     float64         `json:"target_kbps"`
	ActualKbps     float64         `json:"actual_kbps"`
	BitrateErrorPct float64        `json:"bitrate_error_pct"`
	PSNRMeanDB     float64         `json:"psnr_mean_db"`
	SegmentPSNRDB  []float64       `json:"segment_psnr_db"`
	PSNRMaxSwingDB float64         `json:"psnr_max_swing_db"`
	FailedSegments []int           `json:"failed_segments"`
	ReusedSegments []int           `json:"reused_segments"`
	Segments       []SegmentReport `json:"segments"`
}

// Build aggregates a finished (or interrupted) state. reused marks
// segments that were already succeeded when this run started.
func Build(st *job.State, targetKbps float64, reused map[int]bool) *Report {
	r := &Report{Input: st.Input, TargetKbps: targetKbps}
	var bits, dur, psnrWeighted float64
	var frames int
	for _, seg := range st.Segments {
		sr := SegmentReport{
			Index: seg.Index, Status: string(seg.Status), Attempts: seg.Attempts,
			TargetKbps: seg.TargetKbps, ActualKbps: seg.ActualKbps,
			PSNRMeanDB: seg.PSNRMeanDB, PSNRMinDB: seg.PSNRMinDB,
			MaxSwingDB: seg.MaxSwingDB, BelowFloor: seg.BelowFloor,
			Frames: seg.Frames, Error: seg.Err,
		}
		r.Segments = append(r.Segments, sr)
		d := seg.EndSec - seg.StartSec
		switch seg.Status {
		case job.Succeeded:
			bits += seg.ActualKbps * 1000 * d
			dur += d
			r.SegmentPSNRDB = append(r.SegmentPSNRDB, seg.PSNRMeanDB)
			psnrWeighted += seg.PSNRMeanDB * float64(seg.Frames)
			frames += seg.Frames
			if seg.MaxSwingDB > r.PSNRMaxSwingDB {
				r.PSNRMaxSwingDB = seg.MaxSwingDB
			}
			if reused[seg.Index] {
				r.ReusedSegments = append(r.ReusedSegments, seg.Index)
			}
		case job.Failed:
			r.FailedSegments = append(r.FailedSegments, seg.Index)
		}
	}
	if dur > 0 {
		r.ActualKbps = bits / dur / 1000
		r.BitrateErrorPct = (r.ActualKbps - targetKbps) / targetKbps * 100
	}
	if frames > 0 {
		r.PSNRMeanDB = psnrWeighted / float64(frames)
	}
	return r
}

// WriteJSON persists the report to path.
func WriteJSON(r *Report, path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// PrintSummary renders the acceptance metrics to w.
func PrintSummary(w io.Writer, r *Report) {
	fmt.Fprintf(w, "target_kbps:        %.0f\n", r.TargetKbps)
	fmt.Fprintf(w, "actual_kbps:        %.1f\n", r.ActualKbps)
	fmt.Fprintf(w, "bitrate_error_pct:  %+.2f\n", r.BitrateErrorPct)
	fmt.Fprintf(w, "psnr_mean_db:       %.2f\n", r.PSNRMeanDB)
	fmt.Fprintf(w, "segment_psnr_db:    %s\n", formatFloats(r.SegmentPSNRDB))
	fmt.Fprintf(w, "psnr_max_swing_db:  %.2f\n", r.PSNRMaxSwingDB)
	fmt.Fprintf(w, "failed_segments:    %v\n", intOrEmpty(r.FailedSegments))
	if len(r.ReusedSegments) > 0 {
		fmt.Fprintf(w, "reused_segments:    %v\n", r.ReusedSegments)
	}
	for _, s := range r.Segments {
		if s.Error != "" {
			fmt.Fprintf(w, "  seg %d %s (attempts=%d): %s\n", s.Index, s.Status, s.Attempts, s.Error)
		}
	}
}

func formatFloats(v []float64) string {
	s := "["
	for i, f := range v {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%.2f", f)
	}
	return s + "]"
}

func intOrEmpty(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
