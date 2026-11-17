// Package report aggregates per-candidate metrics from the store into
// per-segment and whole-clip quality/bitrate summaries and emits them as
// terminal key=value lines plus machine-readable JSONL.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"rateqa/internal/jobs"
	"rateqa/internal/store"
)

// SegmentReport is the summary for one scene segment.
type SegmentReport struct {
	Segment       int     `json:"segment"`
	SelectedQP    int     `json:"selected_qp"`
	BitrateKbps   float64 `json:"actual_kbps"`
	TargetKbps    float64 `json:"target_kbps"`
	ErrorPct      float64 `json:"bitrate_error_pct"`
	PSNRMean      float64 `json:"psnr_mean_db"`
	PSNRMin       float64 `json:"psnr_min_db"`
	PSNRMax       float64 `json:"psnr_max_db"`
	PSNRStd       float64 `json:"psnr_std_db"`
	Frames        int     `json:"frames"`
	BoundedSearch bool    `json:"bounded_search"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	Rounds        int     `json:"search_rounds"`
}

// Summary is the whole-clip aggregate.
type Summary struct {
	Status          string  `json:"status"`
	Segments        int     `json:"segments"`
	TargetKbps      float64 `json:"target_kbps"`
	ActualKbps      float64 `json:"actual_kbps"`
	BitrateErrorPct float64 `json:"bitrate_error_pct"`
	PSNRMean        float64 `json:"psnr_mean_db"`
	SegmentPSNRMin  float64 `json:"segment_psnr_min_db"`
	SegmentPSNRMax  float64 `json:"segment_psnr_max_db"`
	SegmentPSNRStd  float64 `json:"segment_psnr_std_db"`
	Recovered       int     `json:"recovered_candidates"`
}

// Report bundles segment rows and the clip summary.
type Report struct {
	Summary  Summary         `json:"summary"`
	Segments []SegmentReport `json:"segments"`
}

// Build reads the store and the scheduler results into a Report.
func Build(st *store.Store, results []jobs.SegmentResult) Report {
	rep := Report{Segments: make([]SegmentReport, 0, len(results))}
	params := st.Params()
	means := make([]float64, 0, len(results))
	var weightedBitrate, weightedFrames float64
	minP, maxP := math.Inf(1), math.Inf(-1)
	for _, r := range results {
		row := SegmentReport{
			Segment: r.Index, SelectedQP: r.Selected.QP,
			BitrateKbps: round2(r.Selected.BitrateKbps), TargetKbps: params.TargetKbps,
			ErrorPct: round2(100 * (r.Selected.BitrateKbps - params.TargetKbps) / params.TargetKbps),
			PSNRMean: round2(r.Selected.PSNRdB),
			BoundedSearch: r.Bounded, FallbackReason: r.Reason, Rounds: r.Rounds,
		}
		if m := metricsFor(st, r.Index, r.Selected.QP); m != nil {
			row.Frames = m.Frames
			row.PSNRMin = round2(m.PSNRMin)
			row.PSNRMax = round2(m.PSNRMax)
			row.PSNRStd = round2(m.PSNRStd)
		}
		rep.Segments = append(rep.Segments, row)
		means = append(means, r.Selected.PSNRdB)
		weightedBitrate += r.Selected.BitrateKbps * float64(row.Frames)
		weightedFrames += float64(row.Frames)
		if r.Selected.PSNRdB < minP {
			minP = r.Selected.PSNRdB
		}
		if r.Selected.PSNRdB > maxP {
			maxP = r.Selected.PSNRdB
		}
	}
	if len(rep.Segments) == 0 {
		return rep
	}
	meanPSNR := avg(means)
	actual := weightedBitrate / weightedFrames
	rep.Summary = Summary{
		Status: "complete", Segments: len(rep.Segments), TargetKbps: params.TargetKbps,
		ActualKbps: round2(actual),
		BitrateErrorPct: round2(100 * (actual - params.TargetKbps) / params.TargetKbps),
		PSNRMean:       round2(meanPSNR),
		SegmentPSNRMin: round2(minP), SegmentPSNRMax: round2(maxP),
		SegmentPSNRStd: round2(stdDev(means, meanPSNR)),
		Recovered:      st.Recovered(),
	}
	return rep
}

func metricsFor(st *store.Store, seg, qp int) *store.CandidateMetrics {
	for _, j := range st.Snapshot() {
		if j.Segment == seg && j.QP == qp && j.State == store.StateCompleted {
			m := j.Metrics
			return &m
		}
	}
	return nil
}

// MarkIncomplete downgrades the status when the run did not finish.
func (r *Report) MarkIncomplete() { r.Summary.Status = "incomplete" }

// WriteJSONL writes one JSON object per line (summary first, then segments).
func (r *Report) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	if err := enc.Encode(r.Summary); err != nil {
		return err
	}
	for i := range r.Segments {
		if err := enc.Encode(r.Segments[i]); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSONLFile writes to workspace/report.jsonl.
func (r *Report) WriteJSONLFile(workspace string) (string, error) {
	p := filepath.Join(workspace, "report.jsonl")
	f, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return p, r.WriteJSONL(f)
}

// PrintTerminal writes the human-readable key=value summary.
func (r *Report) PrintTerminal(w io.Writer) {
	s := r.Summary
	fmt.Fprintf(w, "status=%s segments=%d target_kbps=%.0f actual_kbps=%.1f "+
		"bitrate_error_pct=%.2f psnr_mean_db=%.2f segment_psnr_min_db=%.2f "+
		"segment_psnr_max_db=%.2f segment_psnr_std_db=%.2f recovered_candidates=%d\n",
		s.Status, s.Segments, s.TargetKbps, s.ActualKbps, s.BitrateErrorPct,
		s.PSNRMean, s.SegmentPSNRMin, s.SegmentPSNRMax, s.SegmentPSNRStd, s.Recovered)
	for _, sg := range r.Segments {
		fmt.Fprintf(w, "segment=%d qp=%d actual_kbps=%.1f error_pct=%.2f "+
			"psnr_mean=%.2f psnr_min=%.2f psnr_max=%.2f psnr_std=%.2f frames=%d\n",
			sg.Segment, sg.SelectedQP, sg.BitrateKbps, sg.ErrorPct,
			sg.PSNRMean, sg.PSNRMin, sg.PSNRMax, sg.PSNRStd, sg.Frames)
	}
}

func avg(xs []float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func stdDev(xs []float64, mean float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += (x - mean) * (x - mean)
	}
	return math.Sqrt(sum / float64(len(xs)))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
