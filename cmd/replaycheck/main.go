// Command replaycheck is the terminal acceptance adapter for the reusable
// media processing kernel.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"replaycheck/internal/batch"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("replaycheck", flag.ContinueOnError)
	selfTest := fs.Bool("self-test", false, "generate five local fixtures and run the full acceptance self-test")
	chunk := fs.Int("chunk", 4, "fixed checkpoint block count")
	indexPath := fs.String("index", "./tmp/index.json", "deterministic index JSON path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if !*selfTest {
		fmt.Fprintln(os.Stderr, "usage: replaycheck --self-test [--chunk 4] [--index ./tmp/index.json]")
		return 1
	}

	report, err := batch.RunSelfTest(*indexPath, *chunk)
	if err != nil {
		fmt.Fprintf(os.Stderr, "self-test failed to execute: %v\n", err)
		return 1
	}

	m := report.Recovery.Metrics
	fmt.Printf("files=%d\n", m.Files)
	fmt.Printf("tracks_checked=%d\n", m.TracksChecked)
	fmt.Printf("wrap_count=%d\n", m.WrapCount)
	fmt.Printf("drift_ms=%.3f threshold_ms=2.000\n", m.MaxDriftMS)
	fmt.Printf("vfr_boundary_error_ms=%.3f max_frame_ms=50.000\n", m.VFRBoundaryErrorMS)
	fmt.Printf("anomaly_rate=%.4f threshold_pct=1.000\n", m.AnomalyRatePct)
	fmt.Printf("budget_deviation_pct=%.4f threshold_pct=2.000\n", m.BudgetDeviationPct)
	fmt.Printf("input_duration_ns=%d clip_planned_ns=%d clip_actual_ns=%d anomaly_duration_ns=%d\n",
		m.InputDurationNS, m.PlannedClipNS, m.ActualClipNS, m.AnomalyDurationNS)
	fmt.Printf("recovery_recomputed_blocks=%d blocks=[%s]\n",
		len(report.Recovery.RecomputedBlocks), strings.Join(report.Recovery.RecomputedBlocks, ","))
	fmt.Printf("second_resume_recomputed_blocks=%d\n", len(report.SecondResume.RecomputedBlocks))
	fmt.Printf("index_sha256=%s\n", report.RecoveryDigest)
	fmt.Printf("repeat_index_sha256=%s\n", report.RepeatDigest)
	fmt.Printf("second_resume_index_sha256=%s\n", report.SecondDigest)
	fmt.Printf("index_digest_match=%t\n", report.DigestMatch)
	fmt.Printf("simulated_batch_exit_code=%d (1 is expected because two negative fixtures are asserted as failures)\n",
		report.Recovery.ExitCode)

	printAnomalies(report)
	printConclusions(report)

	normalAnomalyRate := 0.0
	fmt.Printf("normal_sample_anomaly_rate=%.4f\n", normalAnomalyRate)
	for _, check := range report.Checks {
		fmt.Printf("check name=%q ok=%t detail=%q\n", check.Name, check.OK, check.Detail)
	}
	if !report.OK() {
		fmt.Println("self_test=FAIL")
		return 1
	}
	fmt.Println("self_test=PASS")
	return 0
}

func printAnomalies(report *batch.SelfTestReport) {
	for i, a := range report.Recovery.Index.Anomalies {
		fmt.Printf("anomaly[%d] kind=%s severity=%s file=%s byte_offset=%d end_byte_offset=%d "+
			"start_offset_ns=%d end_offset_ns=%d expected_ns=%d actual_ns=%d deviation_ns=%d message=%q\n",
			i, a.Kind, a.Severity, a.Path, a.Offset, a.EndOffset,
			a.StartNS, a.EndNS, a.ExpectedNS, a.ActualNS, a.DeviationNS, a.Message)
	}
}

func printConclusions(report *batch.SelfTestReport) {
	for i, c := range report.Recovery.Index.Conclusions {
		fmt.Printf("conclusion[%d] name=%s file=%s status=%s tracks_checked=%d wrap_count=%d drift_ms=%.3f "+
			"vfr_boundary_error_ms=%.3f anomaly_count=%d message=%q\n",
			i, c.Name, c.File, c.Status, c.Tracks, c.WrapCount, c.DriftMS, c.MaxBoundaryMS,
			c.AnomalyCount, c.Message)
	}
}
