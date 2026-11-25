// Package audit builds explainable per-item provenance and acceptance summaries.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediaflow/internal/fault"
	"mediaflow/internal/index"
	"mediaflow/internal/plan"
)

// Status values are stable machine-readable item conclusions.
const (
	StatusPass               = "pass"
	StatusRejected           = "rejected"
	StatusQualityFailed      = "quality_failed"
	StatusBudgetFailed       = "budget_failed"
	StatusPolicyFailed       = "policy_failed"
	StatusChunkFailed        = "chunk_failed"
	StatusCheckpointConflict = "checkpoint_conflict"
	StatusManifestError      = "manifest_error"
	StatusIOError            = "io_error"
)

// ItemReport is the complete terminal and persisted conclusion for one asset.
type ItemReport struct {
	ItemID              string              `json:"item_id"`
	Status              string              `json:"status"`
	QualityScore        float64             `json:"quality_score"`
	QualityMin          float64             `json:"quality_min"`
	QualityPass         bool                `json:"quality_pass"`
	RequiredBitrateKbps int                 `json:"required_bitrate_kbps"`
	TargetBitrateKbps   int                 `json:"target_bitrate_kbps"`
	ActualBitrateKbps   int                 `json:"actual_bitrate_kbps"`
	BudgetMbps          float64             `json:"budget_mbps"`
	ActualMbps          float64             `json:"actual_mbps"`
	BudgetDeviationMbps float64             `json:"budget_deviation_mbps"`
	BitrateWithinTol    bool                `json:"bitrate_within_tolerance"`
	Tolerance           float64             `json:"tolerance"`
	AnomalyCount        int                 `json:"anomaly_count"`
	AnomalyStartMS      int64               `json:"anomaly_start_ms"`
	AnomalyEndMS        int64               `json:"anomaly_end_ms"`
	Intervals           []fault.Interval    `json:"intervals,omitempty"`
	Reasons             []string            `json:"reasons,omitempty"`
	ChunkCount          int                 `json:"chunk_count"`
	CompletedChunks     int                 `json:"completed_chunks"`
	WrittenChunks       []int               `json:"written_chunks,omitempty"`
	SkippedChunks       []int               `json:"skipped_chunks,omitempty"`
	RolledBackChunks    []int               `json:"rolled_back_chunks,omitempty"`
	InputDigest         string              `json:"input_digest"`
	ConfigDigest        string              `json:"config_digest"`
	OutputDigest        string              `json:"output_digest,omitempty"`
	OutputPath          string              `json:"output_path,omitempty"`
	CheckpointPath      string              `json:"checkpoint_path,omitempty"`
	Config              *plan.Config        `json:"config,omitempty"`
	Chunks              []index.ChunkRecord `json:"chunks,omitempty"`
}

// Summary contains every item conclusion, including failures.
type Summary struct {
	BatchKey string        `json:"batch_key"`
	ExitCode int           `json:"exit_code"`
	Items    []*ItemReport `json:"items"`
}

// Add appends an item report.
func (s *Summary) Add(r *ItemReport) { s.Items = append(s.Items, r) }

// FailedCount counts non-pass items.
func (s *Summary) FailedCount() int {
	n := 0
	for _, it := range s.Items {
		if it.Status != StatusPass {
			n++
		}
	}
	return n
}

// RecomputeExit maps item statuses to the required CLI exit code.
func (s *Summary) RecomputeExit() int {
	for _, it := range s.Items {
		switch it.Status {
		case StatusManifestError:
			s.ExitCode = 2
			return 2
		case StatusPass:
		default:
			s.ExitCode = 1
		}
	}
	return s.ExitCode
}

// WriteJSON writes the full summary with stable struct field ordering.
func (s *Summary) WriteJSON(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// PrintText renders the required acceptance fields in a stable terminal format.
func (s *Summary) PrintText(w io.Writer) {
	fmt.Fprintf(w, "BATCH %s items=%d failed=%d exit=%d\n", s.BatchKey, len(s.Items), s.FailedCount(), s.ExitCode)
	for _, it := range s.Items {
		start, end := anomalyRange(it.Intervals)
		fmt.Fprintf(w,
			"ITEM %s status=%s quality=%.4f/min=%.2f pass=%v required_kbps=%d target_kbps=%d actual_kbps=%d budget_mbps=%.2f actual_mbps=%.3f budget_delta=%.3f tol_pass=%v anomalies=%d range_ms=[%d,%d] chunks=%d/%d",
			it.ItemID, it.Status, it.QualityScore, it.QualityMin, it.QualityPass,
			it.RequiredBitrateKbps, it.TargetBitrateKbps, it.ActualBitrateKbps, it.BudgetMbps, it.ActualMbps,
			it.BudgetDeviationMbps, it.BitrateWithinTol, it.AnomalyCount, start, end,
			it.CompletedChunks, it.ChunkCount)
		fmt.Fprintf(w, " in=%s out=%s cfg=%s\n", shortDigest(it.InputDigest), shortDigest(it.OutputDigest), shortDigest(it.ConfigDigest))
		if len(it.WrittenChunks) > 0 || len(it.SkippedChunks) > 0 || len(it.RolledBackChunks) > 0 {
			fmt.Fprintf(w, "  chunks written=%v skipped=%v rolled_back=%v\n", it.WrittenChunks, it.SkippedChunks, it.RolledBackChunks)
		}
		for _, in := range it.Intervals {
			loc := location(in)
			fmt.Fprintf(w, "  anomaly class=%s kind=%s %s time_ms=[%d,%d] reject=%v msg=%s\n",
				in.Class, in.Kind, loc, in.StartMS, in.EndMS, in.Reject, in.Message)
		}
		if it.Config != nil {
			for _, d := range it.Config.Decision.Deviations {
				fmt.Fprintf(w, "  deviation constraint=%s expected=%.4f actual=%.4f delta=%.4f unit=%s\n",
					d.Constraint, d.Expected, d.Actual, d.Delta, d.Unit)
			}
		}
		for _, r := range it.Reasons {
			fmt.Fprintf(w, "  reason: %s\n", r)
		}
	}
}

func anomalyRange(in []fault.Interval) (int64, int64) {
	if len(in) == 0 {
		return 0, 0
	}
	minV := in[0].StartMS
	maxV := in[0].EndMS
	for _, x := range in {
		if x.StartMS < minV {
			minV = x.StartMS
		}
		if x.EndMS > maxV {
			maxV = x.EndMS
		}
	}
	return minV, maxV
}

func location(in fault.Interval) string {
	var b strings.Builder
	if in.TrackID != 0 {
		fmt.Fprintf(&b, "track_id=%d track_no=%d ", in.TrackID, in.TrackNumber)
	}
	if in.EndSample > in.StartSample || in.StartSample != 0 {
		fmt.Fprintf(&b, "samples=[%d,%d) ", in.StartSample, in.EndSample)
	}
	if in.ByteEnd > in.ByteStart {
		fmt.Fprintf(&b, "bytes=[%d,%d) ", in.ByteStart, in.ByteEnd)
	}
	return strings.TrimSpace(b.String())
}

func shortDigest(d string) string {
	if d == "" {
		return "-"
	}
	if len(d) > 22 {
		return d[:22]
	}
	return d
}
