package main

import (
	"encoding/json"
	"io"

	"assetcheck/internal/errmodel"
)

// ReportView is replaced by the full definition in report.go next batch.
type ReportView struct {
	EvalPass bool `json:"eval-pass"`
}

func RenderText(v *ReportView) string { return "" }

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func exitCodeFor(err error) int { return errmodel.ExitCode(err) }

// runAnalyze is replaced by the full orchestration in run.go next batch.
func runAnalyze(opts options) (*ReportView, error) {
	return &ReportView{EvalPass: true}, nil
}
