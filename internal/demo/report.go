// Package report renders pipeline results as the stable terminal lines used by
// the acceptance commands.  Output lives here so the format is testable without
// running a process.
package demo

import (
	"fmt"
	"io"
	"strings"
)

// WriteSummary prints the one-line machine-readable summary and, for damaged or
// rejected runs, the detailed damage, gap, budget and validation records.
func WriteSummary(w io.Writer, r *Result) {
	if r.Resumed || r.InterruptedRun {
		writeResume(w, r)
		return
	}
	fmt.Fprintf(w, "assets=%d missing=%d conflicts=%d validator=%s sha256=%s\n",
		r.Assets, r.Missing, r.Conflicts, validatorWord(r), r.digestOrZero())
	if r.Gaps > 0 {
		fmt.Fprintf(w, "gaps=%d\n", r.Gaps)
	}
	if r.BudgetRejections > 0 {
		fmt.Fprintf(w, "budget_rejections=%d\n", r.BudgetRejections)
	}
	for _, d := range r.Damage {
		fmt.Fprintf(w, "damage offset=%d code=%s: %s\n", d.Offset, d.Code, d.Msg)
	}
	for _, d := range r.ValidationFaults {
		fmt.Fprintf(w, "validation_fail offset=%d code=%s: %s\n", d.Offset, d.Code, d.Msg)
	}
	if r.RolledBack {
		fmt.Fprintf(w, "rollback reason=%s\n", strings.ReplaceAll(r.RollbackReason, "\n", " "))
	}
}

func writeResume(w io.Writer, r *Result) {
	word := validatorWord(r)
	fmt.Fprintf(w, "resumed_units=%d elapsed_ms=%d sha256=%s validator=%s\n",
		r.ResumedUnits, r.ElapsedMS, r.digestOrZero(), word)
	if r.InterruptedRun {
		fmt.Fprintf(w, "interrupted=1 committed=%d\n", r.ResumedUnits)
	}
}

func validatorWord(r *Result) string {
	if r.ValidatorPass {
		return "pass"
	}
	return "fail"
}

func (r *Result) digestOrZero() string {
	if r.SHA256 == "" {
		return strings.Repeat("0", 64)
	}
	return r.SHA256
}
