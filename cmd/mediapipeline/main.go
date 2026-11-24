// Command mediapipeline is the acceptance entry point for the media kernel.
//
// Usage:
//
//	mediapipeline demo --seed N --out PATH
//	    Generates a clean deterministic packet stream and reports packet
//	    counts, duplicates, gaps, A/V offset and the output SHA-256.
//
//	mediapipeline verify --workdir DIR --out PATH
//	    Builds truncated, corrupted, rollover and out-of-window-late fixtures,
//	    simulates a node interruption followed by resume, and reports the first
//	    error byte offset, resume timing, blocking dependency, reused nodes and
//	    the output digest.
//
// Exit code is 0 on success and non-zero for any input, sync, dependency or
// verification failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediapipeline <demo|verify> [flags]")
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 1, "deterministic fixture seed")
	out := fs.String("out", "", "output MP4 path (required)")
	workdir := fs.String("workdir", "", "checkpoint/work directory (default: temp)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("demo requires --out")
	}
	wd := *workdir
	if wd == "" {
		wd = filepath.Join(os.TempDir(), fmt.Sprintf("mediapipeline-demo-%d", *seed))
	}
	cfg := pipeline.Config{
		RunID:              fmt.Sprintf("demo-%d", *seed),
		Seed:               *seed,
		WorkDir:            wd,
		OutPath:            *out,
		JitterWindow:       8,
		SyncToleranceTicks: 1 << 20,
	}
	engine, err := pipeline.NewEngine(cfg)
	if err != nil {
		return err
	}
	rep := engine.Run()
	if rep.FirstError != nil {
		return fmt.Errorf("demo failed: %w", rep.FirstError)
	}
	fmt.Printf("packets=%d\n", rep.Packets)
	fmt.Printf("duplicates=%d\n", rep.Duplicates)
	fmt.Printf("gaps=%d\n", rep.Gaps)
	fmt.Printf("sync_offset_ticks=%d\n", rep.SyncOffsetTicks)
	fmt.Printf("sha256=%s\n", rep.Summary)
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	workdir := fs.String("workdir", "", "working directory for fixtures/checkpoints (required)")
	out := fs.String("out", "", "output MP4 path (required)")
	seed := fs.Int64("seed", 7, "deterministic fixture seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *workdir == "" || *out == "" {
		return fmt.Errorf("verify requires --workdir and --out")
	}
	if err := os.MkdirAll(*workdir, 0o755); err != nil {
		return err
	}
	v := &verifier{workdir: *workdir, out: *out, seed: *seed}
	return v.run()
}

// resumeSlack absorbs timer/scheduling noise when comparing run durations.
const resumeSlack = 25 * time.Millisecond

// verifier holds the acceptance fault-injection and resume scenario.
type verifier struct {
	workdir string
	out     string
	seed    int64
}

func (v *verifier) run() error {
	// 1. Corruption fixture: prove 1-byte localization.
	corruptSpec := fixture.Spec{
		Seed: v.seed, Packets: 24, SampleRate: fixture.OpusRate, StartSeq: 100,
		Corrupt: map[int]int{5: fixtureOffset()},
	}
	corruptRaw, meta, err := fixture.GenerateAudio(corruptSpec)
	if err != nil {
		return err
	}
	injectedOffset := meta.CorruptOffsets[5]
	if err := v.checkCorruption(corruptRaw, injectedOffset); err != nil {
		return err
	}
	fmt.Printf("first_error_offset=%d\n", injectedOffset)

	// 2. Rollover fixture: sequence numbers cross the 16-bit boundary, with a
	// duplicate to prove extended-seq handling across the wrap.
	rolloverSpec := fixture.Spec{
		Seed: v.seed, Packets: 8, SampleRate: fixture.OpusRate, StartSeq: 65532,
		Duplicate: map[int]bool{2: true},
	}
	rolloverRaw, rolloverMeta, err := fixture.GenerateAudio(rolloverSpec)
	if err != nil {
		return err
	}
	if err := v.checkRollover(rolloverRaw, rolloverMeta); err != nil {
		return err
	}

	// 3. Truncation fixture: cut the stream mid-packet and confirm a
	// CatTruncated error carrying a stream-relative byte offset.
	baseSpec := fixture.Spec{Seed: v.seed, Packets: 24, SampleRate: fixture.OpusRate, StartSeq: 100}
	fullRaw, _, err := fixture.GenerateAudio(baseSpec)
	if err != nil {
		return err
	}
	truncSpec := baseSpec
	truncSpec.TruncateAt = len(fullRaw) - 7 // slice through the final packet
	truncRaw, _, err := fixture.GenerateAudio(truncSpec)
	if err != nil {
		return err
	}
	if err := v.checkTruncation(truncRaw, int64(truncSpec.TruncateAt)); err != nil {
		return err
	}

	// 4. Out-of-window late fixture plus a gap (drop) to exercise recovery.
	lateSpec := fixture.Spec{
		Seed: v.seed, Packets: 24, SampleRate: fixture.OpusRate, StartSeq: 100,
		Late: map[int]int{1: 18},
		Drop: map[int]bool{7: true},
	}

	// 5. Baseline: a from-scratch run over the same fixture, timed in full.
	freshStart := time.Now()
	freshCfg := pipeline.Config{
		RunID: "verify-fresh", Seed: v.seed,
		WorkDir: filepath.Join(v.workdir, "fresh"), OutPath: v.out + ".fresh",
		JitterWindow: 4, SyncToleranceTicks: 1 << 20, AudioSpec: &lateSpec,
	}
	freshEngine, err := pipeline.NewEngine(freshCfg)
	if err != nil {
		return err
	}
	freshRep := freshEngine.Run()
	if freshRep.FirstError != nil {
		return fmt.Errorf("fresh run failed: %w", freshRep.FirstError)
	}
	freshElapsed := time.Since(freshStart)

	// 6. Interrupt the pipeline inside the recover node, then resume in a
	// second engine. Only the incomplete node (and its dependents) rerun.
	cfg1 := pipeline.Config{
		RunID: "verify-resume", Seed: v.seed,
		WorkDir: filepath.Join(v.workdir, "resume"), OutPath: v.out,
		JitterWindow: 4, SyncToleranceTicks: 1 << 20,
		AudioSpec:    &lateSpec,
		FailNodeOnce: pipeline.NodeRecover,
	}
	e1, err := pipeline.NewEngine(cfg1)
	if err != nil {
		return err
	}
	rep1 := e1.Run()
	if rep1.FirstError == nil {
		return fmt.Errorf("expected simulated interruption, got success")
	}

	resumeStart := time.Now()
	cfg2 := cfg1
	cfg2.FailNodeOnce = "" // the second attempt commits successfully
	e2, err := pipeline.NewEngine(cfg2)
	if err != nil {
		return err
	}
	rep2 := e2.Run()
	resumeElapsed := time.Since(resumeStart)
	if rep2.FirstError != nil {
		return fmt.Errorf("resume failed: %w", rep2.FirstError)
	}

	blocked := "<none>"
	if rep1.BlockedDependency != nil {
		blocked = rep1.BlockedDependency.Dependency
	}
	fmt.Printf("resume_ms=%d\n", resumeElapsed.Milliseconds())
	fmt.Printf("blocked_dependency=%s\n", blocked)
	fmt.Printf("reused_nodes=%s\n", joinOrEmpty(rep2.ReusedNodes))
	fmt.Printf("sha256=%s\n", rep2.Summary)

	// 7. The resumed run must not be slower than processing the same fixture
	// from scratch (a small slack absorbs timer granularity and scheduling
	// noise), and must reproduce the identical output digest.
	if resumeElapsed > freshElapsed+resumeSlack {
		return fmt.Errorf("resume %v slower than from-scratch run %v",
			resumeElapsed, freshElapsed)
	}
	if rep2.Summary != freshRep.Summary {
		return fmt.Errorf("digest mismatch after retry: %s vs %s",
			rep2.Summary, freshRep.Summary)
	}
	return nil
}
