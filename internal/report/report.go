// Package report renders measurement results as terminal tables and JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// Gates holds user-configurable pass/fail thresholds.
type Gates struct {
	IntegratedMin *float64
	IntegratedMax *float64
	ShortTermMax  *float64
	TruePeakMax   *float64
}

// FileReport is the per-file outcome. Nil pointers serialize as JSON
// null and mean "no valid measurement".
type FileReport struct {
	Path           string   `json:"path"`
	Error          string   `json:"error,omitempty"`
	Channels       int      `json:"channels,omitempty"`
	SampleRate     int      `json:"sample_rate,omitempty"`
	DurationSec    float64  `json:"duration_seconds,omitempty"`
	IntegratedLUFS *float64 `json:"integrated_lufs"`
	ShortTermMax   *float64 `json:"short_term_max_lufs"`
	TruePeakDBTP   *float64 `json:"true_peak_dbtp"`
	BlocksTotal    int      `json:"blocks_total,omitempty"`
	BlocksAbs      int      `json:"blocks_above_absolute_gate,omitempty"`
	BlocksRel      int      `json:"blocks_above_relative_gate,omitempty"`
	LoudnessStatus string   `json:"loudness_status,omitempty"`
	LoudnessPass   *bool    `json:"loudness_pass"`
	TruePeakPass   *bool    `json:"true_peak_pass"`
	OverallPass    *bool    `json:"overall_pass"`
}

// Summary aggregates a batch run.
type Summary struct {
	Total      int `json:"total"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	GateFailed int `json:"gate_failed"`
}

// Evaluate applies gates to a successful measurement. A nil true peak
// (digital silence) passes any maximum; a missing integrated value
// fails any loudness gate.
func (r *FileReport) Evaluate(g Gates) {
	if r.Error != "" {
		return
	}
	loud, tp := true, true
	if g.IntegratedMin != nil || g.IntegratedMax != nil {
		switch {
		case r.IntegratedLUFS == nil:
			loud = false
		case g.IntegratedMin != nil && *r.IntegratedLUFS < *g.IntegratedMin:
			loud = false
		case g.IntegratedMax != nil && *r.IntegratedLUFS > *g.IntegratedMax:
			loud = false
		}
	}
	if g.ShortTermMax != nil && (r.ShortTermMax == nil || *r.ShortTermMax > *g.ShortTermMax) {
		loud = false
	}
	if g.TruePeakMax != nil && r.TruePeakDBTP != nil && *r.TruePeakDBTP > *g.TruePeakMax {
		tp = false
	}
	r.LoudnessPass = &loud
	r.TruePeakPass = &tp
	overall := loud && tp
	r.OverallPass = &overall
}

func fptr(v *float64, unit string) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%.2f %s", *v, unit)
}

func bptr(v *bool) string {
	if v == nil {
		return "-"
	}
	if *v {
		return "PASS"
	}
	return "FAIL"
}

// PrintTable writes a human-readable table plus summary line.
func PrintTable(w io.Writer, reps []FileReport, sum Summary) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tCH\tRATE\tINTEGRATED\tST_MAX\tTRUE_PEAK\tLOUD\tTP\tOVERALL")
	for _, r := range reps {
		if r.Error != "" {
			fmt.Fprintf(tw, "%s\tERROR: %s\n", r.Path, r.Error)
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Path, r.Channels, r.SampleRate,
			fptr(r.IntegratedLUFS, "LUFS"), fptr(r.ShortTermMax, "LUFS"),
			fptr(r.TruePeakDBTP, "dBTP"),
			bptr(r.LoudnessPass), bptr(r.TruePeakPass), bptr(r.OverallPass))
	}
	tw.Flush()
	fmt.Fprintf(w, "Summary: total=%d succeeded=%d failed=%d gate_failed=%d\n",
		sum.Total, sum.Succeeded, sum.Failed, sum.GateFailed)
}

// WriteJSON emits a single-file report as one JSON object.
func WriteJSON(w io.Writer, rep FileReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// BatchJSON is the JSON shape for batch runs.
type BatchJSON struct {
	Files   []FileReport `json:"files"`
	Summary Summary      `json:"summary"`
}

// WriteBatchJSON emits a batch report as one JSON object.
func WriteBatchJSON(w io.Writer, reps []FileReport, sum Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(BatchJSON{Files: reps, Summary: sum})
}
