// Package report renders measurement results as terminal text or JSON.
// It never invents values: unmeasurable loudness is printed as "n/a" in text
// and as JSON null, with an explicit valid flag alongside.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"loudcheck/internal/batch"
	"loudcheck/internal/loudness"
	"loudcheck/internal/policy"
)

// Format selects the output encoding.
type Format string

const (
	Text Format = "text"
	JSON Format = "json"
)

// ParseFormat resolves a --format value.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(s) {
	case "text", "txt", "":
		return Text, nil
	case "json":
		return JSON, nil
	}
	return "", fmt.Errorf("report: unknown format %q (want text or json)", s)
}

// FileReport is the JSON shape of one file's result. Pointers are used for
// optional numbers so that "unknown" serializes as null, not as 0.
type FileReport struct {
	Path            string  `json:"path"`
	Layout          string  `json:"layout,omitempty"`
	Format          string  `json:"format,omitempty"`
	Channels        int     `json:"channels,omitempty"`
	SampleRate      int     `json:"sample_rate,omitempty"`
	Frames          int64   `json:"frames,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`

	IntegratedLUFS        *float64 `json:"integrated_lufs"`
	IntegratedValid       bool     `json:"integrated_valid"`
	ShortTermPeakLUFS     *float64 `json:"short_term_peak_lufs"`
	ShortTermValid        bool     `json:"short_term_valid"`
	ShortTermInsufficient bool     `json:"short_term_insufficient"`
	TruePeakDBTP          *float64 `json:"true_peak_dbtp"`
	SamplePeakDBTP        *float64 `json:"sample_peak_dbtp"`
	GatedBlocks           int      `json:"gated_blocks"`
	TotalBlocks           int      `json:"total_blocks"`

	IntegratedPass bool     `json:"integrated_pass"`
	TruePeakPass   bool     `json:"true_peak_pass"`
	Result         bool     `json:"result"`
	Failures       []string `json:"failures,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// BatchSummary is the JSON shape of a batch run.
type BatchSummary struct {
	Files     int          `json:"files"`
	Passed    int          `json:"passed"`
	NotPassed int          `json:"not_passed"`
	Failed    int          `json:"failed"`
	Result    string       `json:"result"`
	Results   []FileReport `json:"results"`
}

// Build converts a FileReport into its output shape.
func Build(r batch.FileReport) FileReport {
	out := FileReport{
		Path:   r.Path,
		Layout: r.Layout,
		Format: r.Format,
		Error:  r.Err,
	}
	out.IntegratedValid = r.Measure.IntegratedValid
	out.ShortTermValid = r.Measure.ShortTermValid
	out.ShortTermInsufficient = r.Measure.ShortTermInsufficient
	out.GatedBlocks = r.Measure.GatedBlockCount
	out.TotalBlocks = r.Measure.TotalBlockCount
	out.IntegratedPass = r.Verdict.IntegratedPass
	out.TruePeakPass = r.Verdict.TruePeakPass
	out.Result = r.Verdict.Pass
	out.Failures = r.Verdict.Failures
	if r.Err == "" {
		out.Channels = r.Info.Channels
		out.SampleRate = r.Info.SampleRate
		out.Frames = r.Info.Frames
		out.DurationSeconds = r.Info.DurationSecs
		if r.Measure.IntegratedValid {
			v := r.Measure.IntegratedLUFS
			out.IntegratedLUFS = &v
		}
		if r.Measure.ShortTermValid {
			v := r.Measure.ShortTermPeakLUFS
			out.ShortTermPeakLUFS = &v
		}
		if tp := loudness.DB(r.Measure.TruePeak); !math.IsInf(tp, -1) {
			out.TruePeakDBTP = &tp
		}
		if sp := loudness.DB(r.Measure.SamplePeak); !math.IsInf(sp, -1) {
			out.SamplePeakDBTP = &sp
		}
	}
	return out
}

// WriteBatch renders a batch summary in the requested format.
func WriteBatch(w io.Writer, s batch.Summary, f Format) error {
	if f == JSON {
		payload := BatchSummary{
			Files:     len(s.Results),
			Passed:    s.Passed,
			NotPassed: s.NotPassed,
			Failed:    s.Failed,
			Result:    batchResult(s),
			Results:   make([]FileReport, 0, len(s.Results)),
		}
		for _, r := range s.Results {
			payload.Results = append(payload.Results, Build(r))
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}
	return writeText(w, s)
}

// batchResult names the batch outcome without softening partial failure.
func batchResult(s batch.Summary) string {
	switch {
	case s.Failed > 0:
		return "ERROR"
	case s.NotPassed > 0:
		return "FAIL"
	case len(s.Results) == 0:
		return "EMPTY"
	default:
		return "PASS"
	}
}

func writeText(w io.Writer, s batch.Summary) error {
	for _, r := range s.Results {
		if err := writeFileText(w, Build(r)); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "files=%d passed=%d failed_checks=%d errors=%d result=%s\n",
		len(s.Results), s.Passed, s.NotPassed, s.Failed, batchResult(s))
	return nil
}

func writeFileText(w io.Writer, r FileReport) error {
	var b strings.Builder
	name := r.Path
	fmt.Fprintf(&b, "file=%s\n", name)
	if r.Error != "" {
		fmt.Fprintf(&b, "  error=%s\n", r.Error)
		fmt.Fprintf(&b, "  result=ERROR\n")
		_, err := io.WriteString(w, b.String())
		return err
	}
	fmt.Fprintf(&b, "  layout=%s format=%s channels=%d sample_rate=%d frames=%d duration=%.3fs\n",
		r.Layout, r.Format, r.Channels, r.SampleRate, r.Frames, r.DurationSeconds)
	fmt.Fprintf(&b, "  integrated_lufs=%s integrated_valid=%t gated_blocks=%d total_blocks=%d\n",
		fmtLUFS(r.IntegratedLUFS), r.IntegratedValid, r.GatedBlocks, r.TotalBlocks)
	shortNote := ""
	if r.ShortTermInsufficient {
		shortNote = " (insufficient: shorter than 3s)"
	}
	fmt.Fprintf(&b, "  short_term_peak_lufs=%s short_term_valid=%t%s\n",
		fmtLUFS(r.ShortTermPeakLUFS), r.ShortTermValid, shortNote)
	fmt.Fprintf(&b, "  true_peak_dbtp=%s sample_peak_dbtp=%s\n",
		fmtDBTP(r.TruePeakDBTP), fmtDBTP(r.SamplePeakDBTP))
	fmt.Fprintf(&b, "  integrated_pass=%t true_peak_pass=%t result=%s\n",
		r.IntegratedPass, r.TruePeakPass, passName(r.Result))
	for _, f := range r.Failures {
		fmt.Fprintf(&b, "  reason=%s\n", f)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func passName(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func fmtLUFS(p *float64) string {
	if p == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", *p)
}

func fmtDBTP(p *float64) string {
	if p == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", *p)
}

// WriteSingle renders one file's report.
func WriteSingle(w io.Writer, r batch.FileReport, f Format) error {
	if f == JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(Build(r))
	}
	return writeFileText(w, Build(r))
}

// ExitCode maps a batch summary to the documented process exit codes.
func ExitCode(s batch.Summary, formatErr bool) int {
	if formatErr {
		return 2
	}
	if s.Failed > 0 {
		return 3
	}
	if s.AllPassed() {
		return 0
	}
	return 1
}

// ThresholdSummary renders the active criteria once per batch run.
func ThresholdSummary(t policy.Thresholds) string {
	return fmt.Sprintf("thresholds: target=%.2f LUFS tolerance=%.2f LU max_true_peak=%.2f dBTP",
		t.TargetLUFS, t.ToleranceLU, t.MaxTruePeakDBTP)
}
