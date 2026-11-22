// Command mediq is the terminal front end for the broadcast segment
// pipeline. It generates synthetic elementary streams, plans and
// transactionally commits aligned segments, and verifies a workspace
// either fresh or after crash recovery. No network or external media
// tools are used; every artifact is produced locally and offline.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"mediq/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "recover":
		err = runRecover(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		if ec, ok := err.(*exitError); ok {
			fmt.Fprintln(os.Stderr, "error:", ec.Error())
			os.Exit(ec.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func usage() {
	fmt.Fprint(os.Stderr, `mediq - broadcast segment pipeline

usage:
  mediq demo     --work DIR [--seed N] [--inject f1,f2,...] [--fail-after point]
  mediq verify   --work DIR
  mediq recover  --work DIR

faults: truncate, drop-index, duplicate-index, corrupt-unit, misalign
fail-after: scan | plan | commit
exit codes: 0 all invariants hold, 2 damaged input quarantined and safely
committed, 1 recovery or consistency failure (or an injected crash).
`)
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "", "workspace directory")
	seed := fs.Int64("seed", 1, "deterministic sample seed")
	inject := fs.String("inject", "", "comma-separated fault injections")
	failAfter := fs.String("fail-after", "", "crash point: scan|plan|commit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *work == "" {
		return &exitError{1, "--work is required"}
	}
	faults, err := pipeline.ParseFaults(splitCSV(*inject))
	if err != nil {
		return &exitError{1, err.Error()}
	}
	failPoint, err := pipeline.ParseFailPoint(*failAfter)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	cfg := pipeline.DefaultConfig(*work, *seed)
	cfg.Faults = faults
	cfg.FailAfter = failPoint
	out, err := pipeline.Run(cfg)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	printDemoSummary(out)
	if out.StoppedAt != pipeline.FailNone {
		return &exitError{1, fmt.Sprintf("injected crash after %s; run 'mediq recover --work %s'",
			out.StoppedAt, *work)}
	}
	switch out.ExitCode {
	case 2:
		return &exitError{2, "damaged input quarantined and safely committed"}
	case 1:
		return &exitError{1, "consistency failure: group rejected, no segment commit"}
	}
	return nil
}

func runVerify(args []string) error {
	work, err := parseWork(args)
	if err != nil {
		return err
	}
	out, err := pipeline.VerifyWorkspace(work)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	summary := pipeline.VerifyOutcomeReport(out)
	printOrdered(summary)
	fmt.Printf("quarantine_segments=%d\n", countQuarantine(out))
	if len(out.Mismatches) != 0 {
		return &exitError{1, "segment hash verification failed"}
	}
	return nil
}

func runRecover(args []string) error {
	work, err := parseWork(args)
	if err != nil {
		return err
	}
	rep, err := pipeline.RecoverWorkspace(work)
	if err != nil {
		return &exitError{1, err.Error()}
	}
	fmt.Printf("wal_replayed=%d\n", rep.Replayed)
	fmt.Printf("rolled_back=%d\n", rep.RolledBack)
	for _, st := range rep.Stages {
		fmt.Printf("stage=%s state=%s files=%d\n", st.Stage, st.State, len(st.Files))
	}
	// Re-verify after recovery; identical hashes prove the durable
	// commits were never overwritten by half-finished work.
	v, err := pipeline.VerifyWorkspace(work)
	if err != nil {
		fmt.Println("post_recovery_verify=unavailable (no committed manifest yet)")
		return nil
	}
	summary := pipeline.VerifyOutcomeReport(v)
	fmt.Printf("post_recovery_segment_sha256_match=%s\n", summary["segment_sha256_match"])
	if len(v.Mismatches) != 0 {
		return &exitError{1, "post-recovery hash mismatch"}
	}
	return nil
}

func parseWork(args []string) (string, error) {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	work := fs.String("work", "", "workspace directory")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *work == "" {
		return "", &exitError{1, "--work is required"}
	}
	return *work, nil
}

func printDemoSummary(out *pipeline.Outcome) {
	r := out.Report
	fmt.Printf("segments=%d\n", r.Segments)
	fmt.Printf("quarantined_segments=%d\n", r.Quarantined)
	fmt.Printf("roundtrip_consistency=%.2f%%\n", r.RoundtripPct)
	fmt.Printf("segment_sha256_match=%.2f%%\n", r.SHA256MatchPct)
	fmt.Printf("keyframe_index_hit_rate=%.4f\n", r.KeyframeIndexHit)
	fmt.Printf("cross_representation_skew_ms=%.3f\n", r.CrossRepSkewMs)
	fmt.Printf("wal_replayed=%d\n", r.Replayed)
	fmt.Printf("rolled_back=%d\n", r.RolledBack)
	fmt.Printf("injected_truncated=%d\n", r.Injections.TruncatedStreams)
	fmt.Printf("injected_corrupt_unit=%d\n", r.Injections.CorruptUnits)
	fmt.Printf("injected_missing_index=%d\n", r.Injections.MissingIndex)
	fmt.Printf("injected_duplicate_index=%d\n", r.Injections.DuplicateIndex)
	fmt.Printf("injected_misaligned=%d\n", r.Injections.MisalignedReps)
	fmt.Printf("quarantine_records=%d\n", len(r.QuarantineRecords))
	if out.Manifest != nil {
		fmt.Printf("manifest_representations=%d\n", len(out.Manifest.Reps))
	}
}

func printOrdered(m map[string]any) {
	keys := []string{
		"segments", "roundtrip_consistency", "segment_sha256_match",
		"cross_representation_skew_ms", "wal_replayed", "rolled_back",
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			fmt.Printf("%s=%v\n", k, v)
		}
	}
}

func countQuarantine(o *pipeline.VerifyOutcome) int {
	n := 0
	for _, rep := range o.Manifest.Reps {
		for _, s := range rep.Segments {
			if s.Quarantined {
				n++
			}
		}
	}
	return n
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
