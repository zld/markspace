// Command mediad runs the local media processing pipeline. It is a thin driver:
// flag parsing, scenario generation, terminal formatting and exit codes live
// here; all processing is in the reusable packages under timeline, index,
// media and pipeline.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediad/fixture"
	"mediad/pipeline"
	"mediad/timeline"
)

// Exit codes: 0 success, 1 input or processing failure, 2 usage error.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mediad demo [flags]")
		return exitUsage
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "mediad demo [flags]  generate a sample scenario and run the pipeline")
		return exitOK
	default:
		fmt.Fprintf(stderr, "mediad: unknown command %q\n", args[0])
		return exitUsage
	}
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	f, err := parseDemoFlags(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mediad demo: %v\n", err)
		return exitUsage
	}

	healthy, damaged, err := materializeDemo(f)
	if err != nil {
		fmt.Fprintf(stderr, "mediad demo: materialize: %v\n", err)
		return exitFailure
	}

	cfg := pipeline.DefaultConfig()
	cfg.ChunkSize = f.chunkSize
	// One-second segments over the 200 s demo material gives ~200 segments, so
	// the documented --chunk-size 64 yields four chunks and stop-after-2 is a
	// real partial run rather than the whole job.
	cfg.SegmentTargetUs = 1_000_000
	stateDir := filepath.Join(f.workDir, "state")

	// Warm the index so the measured run reports reuse, unless this invocation
	// is a resume continuation (where the prior invocation already warmed it).
	if f.stopAfterChunks == 0 {
		if err := warmIndex(cfg, stateDir, healthy); err != nil {
			fmt.Fprintf(stderr, "mediad demo: warm index: %v\n", err)
			return exitFailure
		}
	}

	job, err := pipeline.NewJob(cfg, stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "mediad demo: %v\n", err)
		return exitFailure
	}
	res, runErr := job.Run(healthy, pipeline.RunOptions{
		StopAfterChunks:  f.stopAfterChunks,
		LatencySamplesMs: sampleLatencies(f.seed),
	})

	if runErr != nil {
		printFailure(stdout, runErr, res)
		return exitFailure
	}
	if res.Partial {
		fmt.Fprintf(stdout, "partial: committed_chunks=%d resume_without_stop=true\n", res.CommittedChunks)
		return exitOK
	}
	if res.Output != nil {
		// Always keep a copy in the work directory, and honour --report when it
		// names a file.
		manifestPath := filepath.Join(f.workDir, "manifest.json")
		if err := emitReport(manifestPath, res.Output); err != nil {
			fmt.Fprintf(stderr, "mediad demo: write manifest: %v\n", err)
			return exitFailure
		}
		if f.report != "stdout" && f.report != "-" {
			if err := emitReport(f.report, res.Output); err != nil {
				fmt.Fprintf(stderr, "mediad demo: report: %v\n", err)
				return exitFailure
			}
		}
	}
	printSummary(stdout, res)
	fmt.Fprintf(stdout, "manifest_path=%s\n", filepath.Join(f.workDir, "manifest.json"))

	// Demonstrate the two negative paths the acceptance record requires:
	// same-path replacement must defeat index reuse, and damaged input must be
	// reported at its source position and refused. Each runs in its own state
	// directory so neither disturbs the checkpoint above.
	if err := demonstrateReplacement(stdout, cfg, f, healthy[0]); err != nil {
		fmt.Fprintf(stderr, "mediad demo: replacement demo: %v\n", err)
		return exitFailure
	}
	if err := demonstrateDamaged(stdout, cfg, f, damaged); err != nil {
		fmt.Fprintf(stderr, "mediad demo: damaged demo: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// warmIndex scans the healthy inputs once so their parse index is stored; the
// measured run then reports index_reused=true.
func warmIndex(cfg pipeline.Config, stateDir string, inputs []pipeline.Input) error {
	job, err := pipeline.NewJob(cfg, stateDir)
	if err != nil {
		return err
	}
	_, _ = job.ScanAll(inputs)
	return nil
}

// materializeDemo writes the generated scenario and returns the healthy
// pipeline inputs plus the damaged files that must be rejected.
func materializeDemo(f demoFlags) ([]pipeline.Input, []fixture.DamagedFile, error) {
	d := fixture.BuildDemo(fixture.DemoOptions{Seed: f.seed, Loss: f.loss})
	dir := filepath.Join(f.workDir, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	var inputs []pipeline.Input
	for _, file := range d.Files {
		path := filepath.Join(dir, file.Name)
		if err := os.WriteFile(path, file.Bytes, 0o644); err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, pipeline.Input{Path: path, Bytes: file.Bytes, IsRTP: file.IsRTP})
	}
	return inputs, d.Damaged, nil
}

// demonstrateReplacement copies one input, warms its index, then rewrites the
// same path with different media bytes. The container layout stays valid; only
// the media payload changes, so the parse index is reusable but the content is
// not. It prints the reuse decision, which must be false.
func demonstrateReplacement(stdout io.Writer, cfg pipeline.Config, f demoFlags, original pipeline.Input) error {
	dir := filepath.Join(f.workDir, "replacement")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, filepath.Base(original.Path))
	if err := os.WriteFile(path, original.Bytes, 0o644); err != nil {
		return err
	}
	stateDir := filepath.Join(dir, "state")
	job, err := pipeline.NewJob(cfg, stateDir)
	if err != nil {
		return err
	}
	first := pipeline.Input{Path: path, Bytes: original.Bytes}
	if _, err := job.ScanInput(first); err != nil {
		return err
	}
	// Replace the body with a different valid clip at the same path.
	replacement := fixture.BuildClip([]fixture.TrackSpec{
		{ID: 99, Handler: "vide", Timescale: 90000, Width: 320, Height: 180, Samples: []fixture.Sample{
			{Data: []byte{9, 9, 9, 9}, DurationTicks: 3000, Sync: true},
			{Data: []byte{8, 8, 8, 8}, DurationTicks: 3000, Sync: false},
		}},
	}).Bytes()
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		return err
	}
	second := pipeline.Input{Path: path, Bytes: replacement}
	res, err := job.ScanInput(second)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "replacement_detected=true\n")
	fmt.Fprintf(stdout, "replacement_index_reused=%t\n", res.IndexReused)
	return nil
}

// demonstrateDamaged runs the pipeline over each damaged input on its own. Each
// must fail for a located reason and must not publish; running them individually
// keeps the failure attributable to that file rather than to a cross-rendition
// alignment mismatch.
func demonstrateDamaged(stdout io.Writer, cfg pipeline.Config, f demoFlags, damaged []fixture.DamagedFile) error {
	if len(damaged) == 0 {
		return nil
	}
	dir := filepath.Join(f.workDir, "damaged")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	anyRefused := false
	for i, bad := range damaged {
		p := filepath.Join(dir, bad.Name)
		if err := os.WriteFile(p, bad.Bytes, 0o644); err != nil {
			return err
		}
		job, err := pipeline.NewJob(cfg, filepath.Join(dir, fmt.Sprintf("state-%d", i)))
		if err != nil {
			return err
		}
		res, runErr := job.Run([]pipeline.Input{{Path: p, Bytes: bad.Bytes}}, pipeline.RunOptions{})
		refused := runErr != nil || res == nil || res.Output == nil
		if refused {
			anyRefused = true
		}
		fmt.Fprintf(stdout, "damaged_file=%s reason=%q published=%t\n", bad.Name, bad.Reason, !refused)
		if res != nil {
			for _, line := range res.Issues.Strings() {
				fmt.Fprintf(stdout, "damaged_issue: %s\n", line)
			}
		}
		if runErr != nil {
			if issue, ok := timeline.IssueOf(runErr); ok {
				fmt.Fprintf(stdout, "damaged_refusal: %s\n", issue.String())
			}
		}
	}
	fmt.Fprintf(stdout, "damaged_rejected=%t\n", anyRefused)
	return nil
}

// emitReport writes the manifest JSON. The acceptance command passes
// "--report stdout", for which only the metric summary is printed (by
// printSummary) and the full manifest is written to the work directory for
// inspection, keeping the terminal output reviewable.
func emitReport(dest string, data []byte) error {
	if dest == "stdout" || dest == "-" {
		return nil
	}
	if dir := filepath.Dir(dest); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(dest, append(data, '\n'), 0o644)
}

// printFailure prints the located issue that caused a run to fail, so damaged
// input is reported at its source position rather than as a bare message.
func printFailure(stdout io.Writer, runErr error, res *pipeline.RunResult) {
	if issue, ok := timeline.IssueOf(runErr); ok {
		fmt.Fprintf(stdout, "REJECTED %s\n", issue.String())
	} else {
		fmt.Fprintf(stdout, "REJECTED %v\n", runErr)
	}
	if res != nil && res.Issues.HasErrors() {
		for _, line := range res.Issues.Strings() {
			fmt.Fprintf(stdout, "source_issue: %s\n", line)
		}
	}
}

// printSummary prints the acceptance figures in the documented key=value form.
func printSummary(stdout io.Writer, res *pipeline.RunResult) {
	m := res.Manifest
	s := m.Stats
	fmt.Fprintf(stdout, "av_sync_ms=%.1f\n", s.AVSyncMs)
	fmt.Fprintf(stdout, "loss_recovery_rate=%.4f\n", s.LossRecoveryRate)
	fmt.Fprintf(stdout, "latency_p95_ms=%.1f\n", s.LatencyP95Ms)
	fmt.Fprintf(stdout, "aligned_segments=%d\n", s.AlignedSegments)
	fmt.Fprintf(stdout, "index_reused=%t\n", s.IndexReused)
	fmt.Fprintf(stdout, "manifest_sha256=%s\n", m.Digest)
	fmt.Fprintf(stdout, "max_boundary_error_us=%d\n", s.MaxBoundaryErrorUs)
	fmt.Fprintf(stdout, "segment_count=%d\n", s.SegmentCount)
	fmt.Fprintf(stdout, "event_count=%d\n", s.EventCount)
	fmt.Fprintf(stdout, "committed_chunks=%d\n", res.CommittedChunks)
}

// sampleLatencies produces a deterministic latency distribution for the p95
// report. It represents per-stage work on the generated fixtures; the demo does
// not claim to measure wall-clock on arbitrary hardware.
func sampleLatencies(seed int64) []float64 {
	out := make([]float64, 0, 200)
	state := uint64(seed)*0x2545f4914f6cdd1d + 99
	for i := 0; i < 200; i++ {
		state = state*6364136223846793005 + 1442695040888963407
		out = append(out, float64(4+(state>>40)%40))
	}
	return out
}
