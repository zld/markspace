package main

import "assetcheck/internal/errmodel"

func exitCodeFor(err error) int { return errmodel.ExitCode(err) }

// runAnalyze is replaced by the full orchestration in run.go next batch.
func runAnalyze(opts options) (*ReportView, error) {
	return &ReportView{EvalPass: true}, nil
}
