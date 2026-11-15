package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"transcode/internal/model"
	"transcode/internal/orchestrator"
)

// JSONReport is the stable machine-readable contract. Field order is fixed
// by struct order; numeric arrays preserve segment order.
type JSONReport struct {
	Status             string              `json:"status"`
	JobID              string              `json:"job_id"`
	Segments           int                 `json:"segments"`
	Succeeded          int                 `json:"succeeded"`
	Failed             int                 `json:"failed"`
	Interrupted        int                 `json:"interrupted"`
	TargetKbps         float64             `json:"target_kbps"`
	ActualKbps         float64             `json:"actual_kbps"`
	DeviationPct       float64             `json:"deviation_pct"`
	PSNRMeanDB         float64             `json:"psnr_mean_db"`
	PSNRMinDB          float64             `json:"psnr_min_db"`
	PSNRMaxDB          float64             `json:"psnr_max_db"`
	WorstSegment       int                 `json:"worst_segment"`
	WorstSegmentPSNRDB float64             `json:"worst_segment_psnr_db"`
	SegmentPSNRDB      []float64           `json:"segment_psnr_db"`
	SegmentDeltaDB     []float64           `json:"segment_psnr_delta_db"`
	Infeasible         []int               `json:"infeasible_segments"`
	Recovery           RecoveryJSON        `json:"recovery"`
	SegmentsDetail     []SegmentDetailJSON `json:"segment_details"`
}

// RecoveryJSON summarizes restart reconciliation.
type RecoveryJSON struct {
	Resumed         bool  `json:"resumed"`
	Invalidated     []int `json:"invalidated_segments"`
	RequeuedRunning []int `json:"requeued_running"`
	RequeuedFailed  []int `json:"requeued_failed"`
}

// SegmentDetailJSON is one stable, per-segment row.
type SegmentDetailJSON struct {
	Segment      int     `json:"segment"`
	Status       string  `json:"status"`
	TargetKbps   float64 `json:"target_kbps"`
	ActualKbps   float64 `json:"actual_kbps"`
	DeviationPct float64 `json:"deviation_pct"`
	Attempts     int     `json:"attempts"`
	PSNRMeanDB   float64 `json:"psnr_mean_db"`
	FailureNote  string  `json:"failure_note,omitempty"`
}

// aggregateActualKbps weights verified segments by duration.
func aggregateActualKbps(task *model.Task) (actual, deviation float64) {
	bits := 0.0
	dur := 0.0
	for _, seg := range task.Segments {
		if seg.Encode != nil && seg.Status == model.StatusSucceeded {
			bits += seg.Encode.ActualKbps * seg.DurationSec
			dur += seg.DurationSec
		}
	}
	if dur == 0 {
		return math.NaN(), math.NaN()
	}
	actual = bits / dur
	deviation = (actual - task.TargetKbps) / task.TargetKbps * 100
	return actual, deviation
}

// BuildReport assembles the JSON-serializable report from a run result.
func BuildReport(res *orchestrator.Result, targetKbps float64) JSONReport {
	task := res.Task
	actual, deviation := aggregateActualKbps(task)
	infeasible := append([]int(nil), res.Infeasible...)
	sort.Ints(infeasible)

	details := make([]SegmentDetailJSON, len(task.Segments))
	for i, seg := range task.Segments {
		d := SegmentDetailJSON{
			Segment: i + 1, Status: string(seg.Status),
			TargetKbps: seg.TargetKbps, Attempts: seg.Attempts,
			PSNRMeanDB: nanOrZero(seg), FailureNote: seg.FailureNote,
		}
		if seg.Encode != nil {
			d.ActualKbps = seg.Encode.ActualKbps
			d.DeviationPct = seg.Encode.Deviation
		} else {
			d.ActualKbps = math.NaN()
			d.DeviationPct = math.NaN()
		}
		details[i] = d
	}

	return JSONReport{
		Status:             overallStatus(res),
		JobID:              task.ID,
		Segments:           len(task.Segments),
		Succeeded:          res.Succeeded,
		Failed:             res.Failed,
		Interrupted:        res.Interrupted,
		TargetKbps:         targetKbps,
		ActualKbps:         actual,
		DeviationPct:       deviation,
		PSNRMeanDB:         res.Quality.MeanPSNRDB,
		PSNRMinDB:          res.Quality.MinPSNRDB,
		PSNRMaxDB:          res.Quality.MaxPSNRDB,
		WorstSegment:       res.Quality.WorstSegment,
		WorstSegmentPSNRDB: res.Quality.WorstSegmentDB,
		SegmentPSNRDB:      res.Quality.SegmentPSNRDB,
		SegmentDeltaDB:     res.Quality.SegmentDeltaDB,
		Infeasible:         infeasible,
		Recovery: RecoveryJSON{
			Resumed:         res.Recovery.Resumed,
			Invalidated:     res.Recovery.Invalidated,
			RequeuedRunning: res.Recovery.RequeuedRunning,
			RequeuedFailed:  res.Recovery.RequeuedFailed,
		},
		SegmentsDetail: details,
	}
}

func overallStatus(res *orchestrator.Result) string {
	switch {
	case res.Interrupted > 0:
		return "interrupted"
	case res.Failed > 0:
		return "partial_failure"
	default:
		return "success"
	}
}

func nanOrZero(seg *model.Segment) float64 {
	if seg.Quality == nil {
		return math.NaN()
	}
	return seg.Quality.MeanPSNRDB
}

// WriteJSON emits the report with stable indentation. Non-finite floats
// (NaN/Inf), which standard JSON cannot represent, are emitted as null so a
// partially failed run still yields a complete, parseable document.
func WriteJSON(w io.Writer, r JSONReport) error {
	// encoding/json rejects NaN/Inf at marshal time; sanitize to null so a
	// partially failed run still yields a complete, parseable document.
	safe, err := sanitize(r)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(out, '\n'))
	return err
}

// sanitize converts the typed report into generic maps with every
// non-finite float replaced by nil (JSON null), preserving key order via an
// ordered builder.
func sanitize(r JSONReport) (map[string]any, error) {
	b := newOrdered()
	b.set("status", r.Status)
	b.set("job_id", r.JobID)
	b.set("segments", r.Segments)
	b.set("succeeded", r.Succeeded)
	b.set("failed", r.Failed)
	b.set("interrupted", r.Interrupted)
	b.set("target_kbps", finiteOrNull(r.TargetKbps))
	b.set("actual_kbps", finiteOrNull(r.ActualKbps))
	b.set("deviation_pct", finiteOrNull(r.DeviationPct))
	b.set("psnr_mean_db", finiteOrNull(r.PSNRMeanDB))
	b.set("psnr_min_db", finiteOrNull(r.PSNRMinDB))
	b.set("psnr_max_db", finiteOrNull(r.PSNRMaxDB))
	b.set("worst_segment", r.WorstSegment)
	b.set("worst_segment_psnr_db", finiteOrNull(r.WorstSegmentPSNRDB))
	b.set("segment_psnr_db", finiteSlice(r.SegmentPSNRDB))
	b.set("segment_psnr_delta_db", finiteSlice(r.SegmentDeltaDB))
	b.set("infeasible_segments", intSliceOrNull(r.Infeasible))
	b.set("recovery", map[string]any{
		"resumed":              r.Recovery.Resumed,
		"invalidated_segments": intSliceOrNull(r.Recovery.Invalidated),
		"requeued_running":     intSliceOrNull(r.Recovery.RequeuedRunning),
		"requeued_failed":      intSliceOrNull(r.Recovery.RequeuedFailed),
	})
	details := make([]map[string]any, len(r.SegmentsDetail))
	for i, d := range r.SegmentsDetail {
		db := newOrdered()
		db.set("segment", d.Segment)
		db.set("status", d.Status)
		db.set("target_kbps", finiteOrNull(d.TargetKbps))
		db.set("actual_kbps", finiteOrNull(d.ActualKbps))
		db.set("deviation_pct", finiteOrNull(d.DeviationPct))
		db.set("attempts", d.Attempts)
		db.set("psnr_mean_db", finiteOrNull(d.PSNRMeanDB))
		if d.FailureNote != "" {
			db.set("failure_note", d.FailureNote)
		}
		details[i] = db.m
	}
	b.set("segment_details", details)
	return b.m, nil
}

func finiteOrNull(v float64) any {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return v
}

func finiteSlice(xs []float64) any {
	if xs == nil {
		return nil
	}
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = finiteOrNull(x)
	}
	return out
}

func intSliceOrNull(xs []int) any {
	if len(xs) == 0 {
		return nil
	}
	return xs
}

// orderedBuilder keeps insertion order for map keys without importing a
// third-party ordered map; Go's encoding/json already sorts map keys, so a
// plain map gives stable output deterministically.
type orderedBuilder struct{ m map[string]any }

func newOrdered() *orderedBuilder             { return &orderedBuilder{m: map[string]any{}} }
func (o *orderedBuilder) set(k string, v any) { o.m[k] = v }

// WriteText emits the human-readable, line-oriented report.
func WriteText(w io.Writer, r JSONReport) {
	fmt.Fprintf(w, "transcode job %s: %s\n", r.JobID, r.Status)
	fmt.Fprintf(w, "segments=%d succeeded=%d failed=%d interrupted=%d\n",
		r.Segments, r.Succeeded, r.Failed, r.Interrupted)
	fmt.Fprintf(w, "target_kbps=%.3f actual_kbps=%.3f deviation_pct=%.4f\n",
		r.TargetKbps, r.ActualKbps, r.DeviationPct)
	fmt.Fprintf(w, "psnr_mean_db=%.4f psnr_min_db=%.4f psnr_max_db=%.4f\n",
		r.PSNRMeanDB, r.PSNRMinDB, r.PSNRMaxDB)
	fmt.Fprintf(w, "worst_segment=%d worst_segment_psnr_db=%.4f\n",
		r.WorstSegment, r.WorstSegmentPSNRDB)
	fmt.Fprintf(w, "segment_psnr_db=%s\n", formatFloats(r.SegmentPSNRDB))
	fmt.Fprintf(w, "segment_psnr_delta_db=%s\n", formatFloats(r.SegmentDeltaDB))
	if len(r.Infeasible) > 0 {
		fmt.Fprintf(w, "infeasible_segments=%v\n", r.Infeasible)
	}
	if r.Recovery.Resumed {
		fmt.Fprintf(w, "recovery: resumed invalidated=%v requeued_running=%v requeued_failed=%v\n",
			r.Recovery.Invalidated, r.Recovery.RequeuedRunning, r.Recovery.RequeuedFailed)
	}
	fmt.Fprintln(w, "segment_details:")
	for _, d := range r.SegmentsDetail {
		fmt.Fprintf(w, "  [%02d] %s target=%.2f actual=%.2f dev=%.3f%% attempts=%d psnr=%s%s\n",
			d.Segment, d.Status, d.TargetKbps, d.ActualKbps, d.DeviationPct,
			d.Attempts, formatFloat(d.PSNRMeanDB),
			note(d.FailureNote))
	}
}

func note(s string) string {
	if s == "" {
		return ""
	}
	return " note=" + s
}

func formatFloats(xs []float64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = formatFloat(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatFloat(x float64) string {
	if math.IsNaN(x) {
		return "NaN"
	}
	if math.IsInf(x, 1) {
		return "+Inf"
	}
	return fmt.Sprintf("%.4f", x)
}
