package main

import (
	"mediaforensics/internal/model"
	"mediaforensics/internal/parse"
	"mediaforensics/internal/validate"
)

func collectErrs(res *parse.Result) []*model.ForensicError {
	if res == nil {
		return nil
	}
	return res.Errors
}

func statusFor(n int) string {
	if n == 0 {
		return "ok"
	}
	return "degraded"
}

func hasHardFailure(r *validate.Report) bool {
	for _, e := range r.Errors {
		if e.Phase == "recovery_rate" || e.Phase == "p95_latency" ||
			e.Phase == "sync_threshold" {
			return true
		}
	}
	return false
}
