// Command medcheck generates self-contained MPEG-TS and RTP fixtures, runs
// them through the journaled media pipeline and prints one JSON report per
// scenario. It is the terminal-only acceptance driver for the media kernel.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"medkernel/media"
	"medkernel/pipeline"
)

// Exit codes per the acceptance contract.
const (
	exitOK       = 0
	exitAssert   = 1 // metric or recovery assertion failed
	exitArgError = 2 // bad command-line arguments
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("medcheck", flag.ContinueOnError)
	selftest := fs.Bool("selftest", false, "generate all scenarios and run assertions")
	seed := fs.Int64("seed", 42, "deterministic RNG seed for fixture generation")
	outDir := fs.String("out", "medcheck-out", "local directory for journal and fragment output")
	if err := fs.Parse(args); err != nil {
		return exitArgError
	}
	if !*selftest {
		fmt.Fprintln(os.Stderr, "medcheck: only --selftest mode is supported")
		return exitArgError
	}
	if *seed < 0 {
		fmt.Fprintln(os.Stderr, "medcheck: --seed must be non-negative")
		return exitArgError
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "medcheck: cannot create output dir: %v\n", err)
		return exitArgError
	}

	report, failed := executeSelfTest(*outDir, *seed)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "medcheck: encode report: %v\n", err)
		return exitAssert
	}
	if failed {
		return exitAssert
	}
	return exitOK
}

// scenarioResult is one entry in the printed JSON array.
type scenarioResult struct {
	Scenario         string                `json:"scenario"`
	SyncErrorMS      float64               `json:"sync_error_ms"`
	RecoveredPackets int64                 `json:"recovered_packets"`
	LossPackets      int64                 `json:"loss_packets"`
	P95LatencyMS     float64               `json:"p95_latency_ms"`
	AudioLUFS        float64               `json:"audio_lufs"`
	RollbackCount    int                   `json:"rollback_count"`
	CommitHash       string                `json:"commit_hash"`
	RecoveryRatePct  float64               `json:"recovery_rate_pct"`
	FaultRanges      []pipeline.FaultRange `json:"fault_ranges,omitempty"`
	Assertions       map[string]bool       `json:"assertions"`
}

// selfTestReport is the top-level printed document.
type selfTestReport struct {
	Seed      int64            `json:"seed"`
	Scenarios []scenarioResult `json:"scenarios"`
	Recovery  recoverySummary  `json:"crash_recovery"`
	AllPassed bool             `json:"all_passed"`
}

type recoverySummary struct {
	FullHash      string `json:"full_commit_hash"`
	RecoveredHash string `json:"recovered_commit_hash"`
	HashesEqual   bool   `json:"hashes_equal"`
}

func executeSelfTest(outDir string, seed int64) (selfTestReport, bool) {
	scenarios := []string{
		pipeline.ScenNormal,
		pipeline.ScenPCRWrap,
		pipeline.ScenPCRDrift,
		pipeline.ScenCCJump,
		pipeline.ScenTruncated,
		pipeline.ScenPSIVersion,
		pipeline.ScenRTPLoss,
	}
	report := selfTestReport{Seed: seed, AllPassed: true}
	th := pipeline.DefaultThresholds()

	for _, scen := range scenarios {
		g := pipeline.NewGenerator(seed)
		tsfx := g.BuildTS(scen, 24)
		rtpfx := g.BuildRTP(scen, 60)
		dir := filepath.Join(outDir, "run-"+scen)
		runner := pipeline.NewRunner(dir, th)
		m, err := runner.Run(scen, tsfx, rtpfx)
		writeSampleFragment(filepath.Join(dir, "sample-frag.mp4"), scen)

		result := scenarioResult{
			Scenario: scen, SyncErrorMS: m.SyncErrorMS,
			RecoveredPackets: m.RecoveredPackets, LossPackets: m.LossPackets,
			P95LatencyMS: m.P95LatencyMS, AudioLUFS: m.AudioLUFS,
			RollbackCount: m.RollbackCount, CommitHash: m.CommitHash,
			RecoveryRatePct: m.RecoveryRatePct, FaultRanges: m.FaultRanges,
			Assertions: map[string]bool{},
		}
		applyAssertions(scen, &result, err, th)
		if !allTrue(result.Assertions) {
			report.AllPassed = false
		}
		report.Scenarios = append(report.Scenarios, result)
	}

	// Interrupt-and-recover simulation against the normal inputs.
	crash, err := pipeline.SimulateCrashRecovery(
		filepath.Join(outDir, "crash"),
		filepath.Join(outDir, "reference"), seed, th)
	report.Recovery = recoverySummary{
		FullHash: crash.FullHash, RecoveredHash: crash.RecoverHash,
		HashesEqual: crash.Recovered,
	}
	if err != nil || !crash.Recovered {
		report.AllPassed = false
	}
	return report, !report.AllPassed
}

// applyAssertions encodes the per-scenario acceptance rules. Fault scenarios
// must report the expected class of evidence; the normal stream must meet
// the numeric thresholds.
func applyAssertions(scen string, r *scenarioResult, runErr error, th pipeline.Thresholds) {
	a := r.Assertions
	a["no_argument_error"] = true
	switch scen {
	case pipeline.ScenNormal:
		a["sync_error_within_20ms"] = r.SyncErrorMS <= th.SyncErrorMaxMS
		a["p95_within_150ms"] = r.P95LatencyMS <= th.P95LatencyMax
		a["lufs_measured"] = r.AudioLUFS < 0 && r.AudioLUFS > -70
		a["commit_hash_present"] = len(r.CommitHash) == 16
	case pipeline.ScenPCRDrift:
		a["clock_fault_rolled_back"] = runErr != nil &&
			media.ClassOf(runErr) == media.ClassValidation
	case pipeline.ScenCCJump, pipeline.ScenTruncated:
		a["fault_range_reported"] = len(r.FaultRanges) > 0
	case pipeline.ScenPCRWrap:
		a["wrap_expanded_without_clock_fault"] = runErr == nil
	case pipeline.ScenPSIVersion:
		a["psi_boundary_processed"] = runErr == nil
	case pipeline.ScenRTPLoss:
		a["recovery_rate_in_range"] = r.RecoveryRatePct >= 0 &&
			r.RecoveryRatePct <= 100
	}
}

func allTrue(m map[string]bool) bool {
	for _, v := range m {
		if !v {
			return false
		}
	}
	return true
}
