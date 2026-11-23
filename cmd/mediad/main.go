// Command mediad is the terminal acceptance adapter for the media pipeline.
// It generates a local demo corpus, runs the clean, damaged, transport,
// manifest-drift, cache-provenance, checkpoint-resume and slow-consumer paths,
// and prints one stable field=value line per metric.
//
// Exit codes: 0 all paths completed and invariants held; 2 bad input or
// arguments; 3 damage was detected and fully diagnosed (only with --strict);
// 4 a recovery or internal invariant failed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"mediamux/internal/fixture"
)

const (
	exitOK       = 0
	exitInput    = 2
	exitCorrupt  = 3
	exitRecovery = 4
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mediad demo [flags]")
		return exitInput
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, "usage: mediad demo --seed N --memory-limit BYTES --slow-consumer-ms N [--strict]")
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return exitInput
	}
}

type demoFlags struct {
	seed         int64
	memoryLimit  int64
	slowConsumer int64
	queueDepth   int
	window       int
	stallMS      int64
	strict       bool
}

func parseFlags(args []string, stderr io.Writer) (*demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := &demoFlags{}
	fs.Int64Var(&f.seed, "seed", 7, "deterministic fixture seed")
	fs.Int64Var(&f.memoryLimit, "memory-limit", 1<<20, "hard ceiling on live media bytes")
	fs.Int64Var(&f.slowConsumer, "slow-consumer-ms", 20, "producer block budget before the branch stops")
	fs.IntVar(&f.queueDepth, "queue-depth", 2, "bounded queue capacity in segments")
	fs.IntVar(&f.window, "window", 3, "sliding window for bitrate statistics")
	fs.Int64Var(&f.stallMS, "stall-ms", 0, "consumer stall used by the slow-consumer path (0 derives it)")
	fs.BoolVar(&f.strict, "strict", false, "exit 3 when the demo diagnoses damage")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	switch {
	case f.seed < 0:
		return nil, errors.New("--seed must be non-negative")
	case f.memoryLimit <= 0:
		return nil, errors.New("--memory-limit must be positive")
	case f.slowConsumer < 0:
		return nil, errors.New("--slow-consumer-ms must not be negative")
	case f.queueDepth <= 0:
		return nil, errors.New("--queue-depth must be positive")
	case f.window <= 0:
		return nil, errors.New("--window must be positive")
	case f.stallMS < 0:
		return nil, errors.New("--stall-ms must not be negative")
	}
	return f, nil
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mediad demo: %v\n", err)
		return exitInput
	}
	var sb strings.Builder
	code, err := executeDemo(context.Background(), f, &sb)
	if err != nil {
		fmt.Fprintf(stderr, "mediad demo: %v\n", err)
		if code == exitOK {
			code = exitRecovery
		}
	}
	io.WriteString(stdout, sb.String())
	return code
}

// executeDemo runs every path and writes the acceptance lines. The returned
// code is the process exit status.
func executeDemo(ctx context.Context, f *demoFlags, out *strings.Builder) (int, error) {
	demo, err := fixture.BuildDemo(f.seed)
	if err != nil {
		return exitInput, err
	}
	fmt.Fprintf(out, "seed=%d\n", f.seed)
	fmt.Fprintf(out, "segments=%d\n", len(demo.Segments))
	fmt.Fprintf(out, "target_bps=%d\n", demo.TargetBPS)
	fmt.Fprintf(out, "memory_limit_bytes=%d\n", f.memoryLimit)
	fmt.Fprintf(out, "slow_consumer_budget_ms=%d\n", f.slowConsumer)

	tmp, err := os.MkdirTemp("", "mediad-demo-")
	if err != nil {
		return exitRecovery, err
	}
	defer os.RemoveAll(tmp)

	p := newPaths(tmp, f)

	clean, err := p.runClean(ctx, demo)
	if err != nil {
		return classify(err), err
	}
	emitRun(out, "clean", clean)

	damaged, err := p.runDamaged(ctx, demo)
	if err != nil {
		return classify(err), err
	}
	emitRun(out, "damaged", damaged)

	dup, err := p.runTransport(ctx, demo, false)
	if err != nil {
		return classify(err), err
	}
	loss, err := p.runTransport(ctx, demo, true)
	if err != nil {
		return classify(err), err
	}

	drift, err := p.runDrift(ctx, demo)
	if err != nil {
		return classify(err), err
	}
	resume, fresh, err := p.runResume(ctx, demo)
	if err != nil {
		return classify(err), err
	}
	forged, err := p.runForged(ctx, demo)
	if err != nil {
		return classify(err), err
	}
	replay, err := p.runReplay(ctx, demo)
	if err != nil {
		return classify(err), err
	}
	slow, err := p.runSlow(ctx, demo, f.stallMS)
	if err != nil {
		return classify(err), err
	}

	// Acceptance metrics. The clean path supplies the normal-sample values;
	// each fault path contributes its own evidence.
	q := clean.Quality
	fmt.Fprintf(out, "actual_bps=%d\n", q.ActualBPS)
	fmt.Fprintf(out, "bitrate_deviation_pct=%.2f\n", q.BitrateDeviationPct)
	fmt.Fprintf(out, "quality_samples=%d\n", q.QualitySamples)
	fmt.Fprintf(out, "keyframe_gap_p50_ms=%d\n", q.KeyframeGapP50MS)
	fmt.Fprintf(out, "keyframe_gap_p95_ms=%d\n", q.KeyframeGapP95MS)
	fmt.Fprintf(out, "corrupt_segments=%d\n", damaged.CorruptSegments)
	fmt.Fprintf(out, "manifest_alignment_error_ms=%d\n", q.AlignmentErrorMS)
	fmt.Fprintf(out, "manifest_drift_segments=%d\n", len(drift.Report.Mismatches))
	fmt.Fprintf(out, "manifest_drift_error_ms=%d\n", drift.Report.TotalDeltaMS)
	if len(drift.Report.Mismatches) > 0 {
		m := drift.Report.Mismatches[0]
		fmt.Fprintf(out, "manifest_drift_ref=segment=%d offset=%d declared_ms=%d actual_ms=%d\n",
			m.Segment, m.ManifestOffset, m.DeclaredMS, m.ActualMS)
	}
	fmt.Fprintf(out, "rtp_duplicates=%d\n", dup.Quality.RTPDuplicates)
	fmt.Fprintf(out, "rtp_duplicate_bytes_excluded=%d\n", dupExcludedBytes(dup))
	fmt.Fprintf(out, "rtp_missing_packets=%d\n", loss.Quality.RTPMissing)
	fmt.Fprintf(out, "rtp_missing_ms=%d\n", loss.Quality.RTPMissingMS)
	fmt.Fprintf(out, "rtp_gap_events=%d\n", countGaps(loss))
	fmt.Fprintf(out, "rtp_transport_only_segments=%d\n", transportOnly(loss))
	fmt.Fprintf(out, "cache_hits=%d\n", clean.Cache.Hits+replay.Cache.Hits)
	fmt.Fprintf(out, "cache_replay_from_cache=%t\n", replayAllFromCache(replay))
	fmt.Fprintf(out, "cache_source_rejects=%d\n", forged.Cache.SourceRejects)
	fmt.Fprintf(out, "checkpoint_resumes=%d\n", boolToInt(resume.Resume.Resumed && resume.Resume.FromSegment > 0))
	fmt.Fprintf(out, "checkpoint_resume_from=%d\n", resume.Resume.FromSegment)
	fmt.Fprintf(out, "resume_digest_matches_full=%t\n", resume.ResultSHA256 == fresh.ResultSHA256)
	fmt.Fprintf(out, "slow_consumer_timed_out=%t\n", slow.SlowConsumer)
	slowCursor := -1
	if slow.Checkpoint != nil {
		slowCursor = slow.Checkpoint.Cursor
		fmt.Fprintf(out, "slow_consumer_checkpoint_consistent=%t\n",
			slow.Checkpoint.Cursor == slow.Checkpoint.LastCompleteIdx+1)
	}
	fmt.Fprintf(out, "slow_consumer_checkpoint_cursor=%d\n", slowCursor)
	peak := maxOf(clean.PeakBufferBytes, damaged.PeakBufferBytes, slow.PeakBufferBytes)
	fmt.Fprintf(out, "peak_buffer_bytes=%d\n", peak)
	fmt.Fprintf(out, "result_sha256=%s\n", clean.ResultSHA256)

	if err := checkInvariants(f, clean, damaged, drift, resume, fresh, forged, slow, peak); err != nil {
		fmt.Fprintf(out, "invariants=FAILED\n")
		fmt.Fprintf(out, "invariant_detail=%s\n", err)
		return exitRecovery, nil
	}
	fmt.Fprintf(out, "invariants=ok\n")

	if damaged.CorruptSegments > 0 || len(drift.Report.Mismatches) > 0 || forged.Cache.SourceRejects > 0 {
		fmt.Fprintf(out, "outcome=damage_diagnosed\n")
		if f.strict {
			return exitCorrupt, nil
		}
		return exitOK, nil
	}
	fmt.Fprintf(out, "outcome=clean\n")
	return exitOK, nil
}
