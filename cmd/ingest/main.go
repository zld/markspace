// Command ingest is the terminal-only demonstration adapter for the media
// pipeline. It generates synthetic success/corrupt/recovery cases locally,
// prints the structured acceptance fields, and maps failures to exit codes.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/pipeline"
	"mediapipeline/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	selfTest := fs.Bool("self-test", false, "run the built-in three-path self test")
	seed := fs.Int64("seed", 42, "deterministic generator seed")
	jitterMS := fs.Int("jitter-ms", 80, "synthetic arrival jitter window in milliseconds")
	loss := fs.Int("loss", 3, "whole-percent stochastic packet loss")
	interrupt := fs.String("interrupt-after", "audio-normalize",
		"stage after which a crash is simulated (written but not committed)")
	workDir := fs.String("work-dir", "", "project temp root (defaults to a local .pipeline-tmp)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*selfTest {
		fmt.Fprintln(os.Stderr, "ingest: only --self-test is supported in this offline build")
		return 2
	}
	base := *workDir
	if base == "" {
		// Keep every temporary file inside the project working tree, never an
		// absolute system temp path, and never printed in the output.
		base = filepath.Join(".pipeline-tmp", fmt.Sprintf("seed-%d", *seed))
	}
	if filepath.IsAbs(base) {
		fmt.Fprintf(os.Stderr, "ingest: work-dir must be a relative project path, got %q\n", base)
		return 2
	}
	opts := pipeline.Options{
		Seed: *seed, JitterMS: *jitterMS, LossPct: *loss,
		InterruptAfter: *interrupt, WorkDir: base,
	}
	result, err := pipeline.Run(opts)
	if err != nil {
		if isConfigErr(err) {
			fmt.Fprintf(os.Stderr, "ingest: %v\n", err)
			return 2
		}
		fmt.Fprintf(os.Stderr, "ingest: invariant failure: %v\n", err)
		return 1
	}
	rep := result.Report
	if err := verifyInvariants(rep); err != nil {
		fmt.Fprintf(os.Stderr, "ingest: %v\n", err)
		return 1
	}
	fmt.Print(rep.Text())
	return 0
}

// verifyInvariants enforces the acceptance contract before printing success.
func verifyInvariants(r report.Report) error {
	if r.CaseCount != 3 {
		return fmt.Errorf("case_count = %d, want 3", r.CaseCount)
	}
	if r.FaultCount != 5 {
		return fmt.Errorf("fault_count = %d, want 5", r.FaultCount)
	}
	if r.EarliestBadPacket < 990 || r.EarliestBadPacket > 1030 {
		return fmt.Errorf("earliest_bad_packet = %d, want around 1000", r.EarliestBadPacket)
	}
	// locate_error_ns must be no more than one microsecond off the shared base.
	if r.LocateErrorNS < 0 || r.LocateErrorNS > 1_000_000 {
		return fmt.Errorf("locate_error_ns = %d exceeds 1e6 bound", r.LocateErrorNS)
	}
	if r.RecoveryMS >= 500 {
		return fmt.Errorf("recovery_ms = %d, want < 500", r.RecoveryMS)
	}
	if r.RollbackStage == "" {
		return fmt.Errorf("rollback stage must be set")
	}
	// commit_boundary is empty only when the very first stage was interrupted,
	// in which case there is no prior complete commit; render that as <none>.
	if r.CommitBoundary == "" && r.RollbackStage != "rtp-demux" {
		return fmt.Errorf("commit boundary must be set after the first stage")
	}
	if err := report.CheckHexSHA256(r.SHA256); err != nil {
		return err
	}
	if !r.RepeatSHAEqual {
		return fmt.Errorf("repeat sha256 mismatch")
	}
	return nil
}

func isConfigErr(err error) bool {
	return err != nil && contains(err.Error(), "configuration error")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
