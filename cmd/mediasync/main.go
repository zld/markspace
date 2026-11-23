// Command mediasync is the offline batch entry point for the local media
// processing kernel. It generates self-contained healthy/damaged samples,
// repairs timestamps, recovers RTP backhaul gaps, builds a random-access
// index and prints machine-checkable summary lines.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/mederr"
	"mediasync/internal/pipeline"
	"mediasync/internal/verify"
)

const (
	defaultSegments     = 24
	defaultFramesPerSeg = 32
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return mederr.ExitInvalidInput
	}
	fs := flag.NewFlagSet("mediasync", flag.ContinueOnError)
	seed := fs.Int64("seed", 23, "deterministic fixture seed")
	chunkRecords := fs.Int("chunk-records", 256, "index records per checkpoint chunk")
	interruptAfter := fs.Int("interrupt-after", 0,
		"stop after N committed chunks (0 = run to completion)")
	resume := fs.Bool("resume", false, "resume from the last valid checkpoint")
	workRoot := fs.String("work", "work", "workspace directory (bounded path)")
	maxRecords := fs.Int("max-records", 1_000_000, "max index records per batch")
	if err := fs.Parse(args[1:]); err != nil {
		return mederr.ExitInvalidInput
	}
	if *chunkRecords <= 0 || *maxRecords <= 0 ||
		*maxRecords > 1_000_000 || *seed < 0 || *interruptAfter < 0 {
		fmt.Fprintln(os.Stderr,
			"invalid flags: chunk-records and limits must be positive")
		return mederr.ExitInvalidInput
	}

	cfg := pipeline.Config{
		WorkspaceRoot:  mustAbs("."),
		WorkDir:        mustAbs(*workRoot),
		Seed:           *seed,
		Segments:       defaultSegments,
		FramesPerSeg:   defaultFramesPerSeg,
		ChunkRecords:   *chunkRecords,
		MaxRecords:     *maxRecords,
		InterruptAfter: *interruptAfter,
		Resume:         *resume,
	}

	switch args[0] {
	case "demo":
		return runDemo(cfg)
	case "verify":
		return runVerify(cfg)
	case "-h", "--help", "help":
		usage()
		return mederr.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return mederr.ExitInvalidInput
	}
}

// runDemo executes the interrupted+resumed scenario for the damaged sample
// and a clean full run for the healthy baseline, printing both summaries.
func runDemo(cfg pipeline.Config) int {
	fmt.Println("== mediasync demo: healthy + damaged self-generated samples ==")
	fmt.Printf("seed=%d chunk_records=%d interrupt_after=%d resume=%t\n",
		cfg.Seed, cfg.ChunkRecords, cfg.InterruptAfter, cfg.Resume)

	// Healthy baseline: no faults, clean full run.
	healthy := cfg
	healthy.WorkDir = filepath.Join(cfg.WorkDir, "healthy")
	healthy.Healthy = true
	healthy.InterruptAfter = 0
	healthy.Resume = false
	hRes, err := pipeline.Run(healthy)
	if err != nil {
		return reportError(err)
	}
	printSummary("healthy", hRes.Summary)

	// Damaged sample: phase 1 interrupted, phase 2 resumed.
	damaged := cfg
	damaged.WorkDir = filepath.Join(cfg.WorkDir, "damaged")
	damaged.Healthy = false
	phaseInterrupted := false
	if damaged.InterruptAfter > 0 {
		phase1 := damaged
		phase1.Resume = false
		res, err := pipeline.Run(phase1)
		if err != nil {
			return reportError(err)
		}
		fmt.Printf("phase1_interrupted=%t recovered_records=%d\n",
			res.Interrupted, res.RecoveredChunks)
		if !res.Interrupted {
			fmt.Fprintln(os.Stderr,
				"interrupt boundary beyond available chunks; rerun with a smaller --interrupt-after")
			return mederr.ExitInvalidInput
		}
		phaseInterrupted = true
	}
	damaged.InterruptAfter = 0
	damaged.Resume = phaseInterrupted
	dRes, err := pipeline.Run(damaged)
	if err != nil {
		return reportError(err)
	}
	if dRes.Interrupted {
		fmt.Fprintln(os.Stderr, "internal error: resume phase still interrupted")
		return mederr.ExitInternal
	}
	printSummary("damaged+resumed", dRes.Summary)
	if err := assertThresholds(dRes.Summary); err != nil {
		fmt.Fprintf(os.Stderr, "threshold failure: %v\n", err)
		return mederr.ExitUnrecoverable
	}
	// Mandated invariant: the resumed index must equal a clean full run.
	fullDamaged := damaged
	fullDamaged.Resume = false
	fullDamaged.WorkDir = filepath.Join(cfg.WorkDir, "damaged-full")
	fullRes, err := pipeline.Run(fullDamaged)
	if err != nil {
		return reportError(err)
	}
	fmt.Printf("resume_index_sha256=%s\n", dRes.Summary.IndexSHA256)
	fmt.Printf("fullrun_index_sha256=%s\n", fullRes.Summary.IndexSHA256)
	if dRes.Summary.IndexSHA256 != fullRes.Summary.IndexSHA256 {
		fmt.Fprintln(os.Stderr,
			"resume/full-run index sha256 mismatch")
		return mederr.ExitUnrecoverable
	}
	fmt.Println("resume_fullrun_digest_match=true")
	fmt.Println("demo_status=ok")
	return mederr.ExitOK
}

// runVerify performs clean full runs of both samples and independently
// re-verifies that index digests match across repeated runs.
func runVerify(cfg pipeline.Config) int {
	fmt.Println("== mediasync verify: clean full-run repeatability ==")
	scenario := func(label string, healthy bool) (pipeline.Config, error) {
		c := cfg
		c.Healthy = healthy
		c.InterruptAfter = 0
		c.Resume = false
		c.WorkDir = filepath.Join(cfg.WorkDir, label)
		first, err := pipeline.Run(c)
		if err != nil {
			return c, err
		}
		second, err := pipeline.Run(c)
		if err != nil {
			return c, err
		}
		printSummary(label+"-run-1", first.Summary)
		printSummary(label+"-run-2", second.Summary)
		if first.Summary.IndexSHA256 != second.Summary.IndexSHA256 {
			return c, fmt.Errorf("%s repeatability: index sha256 differs", label)
		}
		return c, nil
	}
	if _, err := scenario("healthy", true); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return mederr.ExitUnrecoverable
	}
	damagedCfg, err := scenario("damaged", false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return mederr.ExitUnrecoverable
	}
	// Re-derive the damaged summary for threshold gating.
	res, err := pipeline.Run(damagedCfg)
	if err != nil {
		return reportError(err)
	}
	if err := assertThresholds(res.Summary); err != nil {
		fmt.Fprintf(os.Stderr, "threshold failure: %v\n", err)
		return mederr.ExitUnrecoverable
	}
	fmt.Println("verify_status=ok")
	return mederr.ExitOK
}

// printSummary emits one line per checkable field with stable key names.
func printSummary(label string, s verify.Summary) {
	fmt.Printf("-- summary[%s] --\n", label)
	for _, line := range summaryLines(s) {
		fmt.Println(line)
	}
}

// summaryLines renders the mandated checkable fields in a fixed order.
func summaryLines(s verify.Summary) []string {
	return []string{
		fmt.Sprintf("av_sync_error_ms_median=%.3f", s.SyncErrorMedianMS),
		fmt.Sprintf("av_sync_error_ms_p95=%.3f", s.SyncErrorP95MS),
		fmt.Sprintf("packet_recovery_rate=%.4f", s.RecoveryRate),
		fmt.Sprintf("pipeline_latency_p95_ms=%.3f", s.LatencyP95MS),
		fmt.Sprintf("timestamp_wraps=%d", s.TimestampWraps),
		fmt.Sprintf("vfr_intervals=%d", s.VFRIntervals),
		fmt.Sprintf("truncated_segments=%d", s.TruncatedSegments),
		fmt.Sprintf("checkpoint_recovered=%t", s.CheckpointRecovered),
		fmt.Sprintf("index_records=%d", s.IndexRecords),
		fmt.Sprintf("index_sha256=%s", s.IndexSHA256),
		fmt.Sprintf("invariants_ok=%t", s.InvariantsOK),
	}
}

// assertThresholds enforces the acceptance gates from the specification.
func assertThresholds(s verify.Summary) error {
	type gate struct {
		name string
		bad  bool
	}
	gates := []gate{
		{"av_sync median <=8ms", s.SyncErrorMedianMS > 8},
		{"av_sync p95 <=20ms", s.SyncErrorP95MS > 20},
		{"recovery rate >=0.97", s.RecoveryRate < 0.97},
		{"latency p95 <=250ms", s.LatencyP95MS > 250},
		{"timestamp_wraps >=1", s.TimestampWraps < 1},
		{"vfr_intervals >=3", s.VFRIntervals < 3},
		{"truncated_segments >=1", s.TruncatedSegments < 1},
		{"index_records >=512", s.IndexRecords < 512},
		{"invariants_ok", !s.InvariantsOK},
		{"index_sha256 length 64", len(s.IndexSHA256) != 64},
	}
	for _, g := range gates {
		if g.bad {
			return fmt.Errorf("gate failed: %s", g.name)
		}
	}
	return nil
}

func reportError(err error) int {
	code := mederr.ExitCode(err)
	fmt.Fprintf(os.Stderr, "error(exit=%d): %v\n", code, err)
	return code
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func usage() {
	fmt.Fprintln(os.Stderr,
		"usage: mediasync <demo|verify> [--seed N] [--chunk-records N] "+
			"[--interrupt-after N] [--resume] [--work DIR]")
}
