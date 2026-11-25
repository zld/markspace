package demo

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Render writes the full human-readable report for every scenario. The
// output is deterministic: no file paths beyond the scenario name and no
// wall-clock timestamps appear, so identical seeds render byte-identically.
func Render(w io.Writer, results []ScenarioResult) {
	for _, res := range results {
		fmt.Fprintf(w, "=== scenario: %s ===\n", res.Name)
		fmt.Fprintf(w, "codec=%s ssrc=%d clock_rate=%d time_trusted=%t\n",
			res.Report.Codec, res.Report.SSRC, res.Report.ClockRate, res.Report.TimeTrusted)
		renderSamples(w, res)
		renderFaults(w, res)
		renderFacts(w, res)
		fmt.Fprintln(w)
	}
}

// StableSummary renders only the path/time-free digest lines. It is the
// section the acceptance harness compares across two same-seed runs.
func StableSummary(w io.Writer, results []ScenarioResult) {
	fmt.Fprintln(w, "--- stable summary ---")
	for _, res := range results {
		fmt.Fprintf(w, "%s digest=%s\n", res.Name, res.Digest)
		for _, fact := range res.Facts {
			fmt.Fprintf(w, "  %s\n", fact)
		}
	}
}

func renderSamples(w io.Writer, res ScenarioResult) {
	fmt.Fprintln(w, "samples:")
	fmt.Fprintln(w, "  idx | input_seq | bytes(offset:len) | time(start..end,dur) | key | faults")
	for _, s := range res.Report.Samples {
		faults := strings.Join(sortedFaults(s.Faults), ",")
		if faults == "" {
			faults = "-"
		}
		key := "."
		if s.KeyFrame {
			key = "K"
		}
		fmt.Fprintf(w, "  %3d | %d-%d | %d:%d | %d..%d (%d) | %s | %s\n",
			s.Index, s.FirstSeq, s.LastSeq,
			s.ByteOffset, s.ByteLength,
			s.TimeStart, s.TimeEnd, s.Duration,
			key, faults)
	}
}

func renderFaults(w io.Writer, res ScenarioResult) {
	if len(res.Report.Faults) == 0 {
		fmt.Fprintln(w, "faults: none")
		return
	}
	fmt.Fprintln(w, "faults:")
	for _, f := range res.Report.Faults {
		fmt.Fprintf(w, "  class=%s seq=%d-%d byte=%d:%d time=%d..%d",
			f.Class, f.Seq.First, f.Seq.Last,
			f.Byte.Offset, f.Byte.Length, f.Time.Start, f.Time.End)
		if f.OverWindow {
			fmt.Fprint(w, " over_window")
		}
		if f.Detail != "" {
			fmt.Fprintf(w, " detail=%q", f.Detail)
		}
		fmt.Fprintln(w)
	}
}

func renderFacts(w io.Writer, res ScenarioResult) {
	fmt.Fprintln(w, "summary:")
	for _, fact := range res.Facts {
		fmt.Fprintf(w, "  %s\n", fact)
	}
}

// sortedFaults returns fault class names in stable alphabetical order so
// map-backed sample rows render identically on every run.
func sortedFaults(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// ExitCode derives the process exit code from all scenario outcomes.
// Migration rollback wins over damage reporting; usage errors are the
// caller's responsibility and never reach this function.
func ExitCode(results []ScenarioResult) int {
	migrationFailed := false
	damagePresent := false
	for _, r := range results {
		if r.MigrateRolledBack {
			migrationFailed = true
		}
		if len(r.Report.Faults) > 0 {
			damagePresent = true
		}
	}
	switch {
	case migrationFailed:
		return ExitMigrateFailed
	case damagePresent:
		return ExitDamagedReport
	default:
		return ExitOK
	}
}
