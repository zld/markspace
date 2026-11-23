// Command restore runs the offline media-forensics pipeline end to end:
// deterministic fixture generation, strict parsing, index rebuild, repair,
// RTP recovery, headless frame rendering, validation and manifest output.
//
// Exit codes (process-level contract for the self-test driver):
//
//	0 — every path satisfies its quantitative thresholds. Damage that was
//	    recovered per policy still reports status=ok here; the per-case
//	    manifests keep their own exit_code=2 as auditable evidence.
//	2 — a single-input run was damaged but recovered per policy and all
//	    evidence was written (reserved for future single-input mode).
//	1 — an invariant or quantitative threshold failed.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediaforensics/internal/render"
	"mediaforensics/internal/validate"
)

func main() {
	selfTest := flag.Bool("self-test", false, "run all built-in fixture cases")
	seed := flag.Int64("seed", 42, "deterministic generator seed")
	frames := flag.Int("frames", 120, "output frame count (max 10000)")
	loss := flag.Float64("packet-loss", 0.08, "synthetic RTP packet loss fraction")
	output := flag.String("output", "/tmp/archive-restore", "evidence output directory")
	flag.Parse()

	if !*selfTest {
		fmt.Fprintln(os.Stderr, "only --self-test mode is supported in this offline build")
		os.Exit(1)
	}
	if *frames > render.MaxFrames {
		fmt.Fprintf(os.Stderr, "frames %d exceeds cap %d\n", *frames, render.MaxFrames)
		os.Exit(1)
	}

	results, primary, err := runAllCases(*output, *seed, *frames, *loss)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	for _, r := range results {
		fmt.Printf("case=%s offset=%d recovered_rate=%.4f sync_error_ms=%.3f p95_latency_ms=%.3f\n",
			r.name, r.offset, r.recoveredRate, r.syncErrorMs, r.p95LatencyMs)
	}
	// Threshold failures are hard failures (exit 1). Damage handled per
	// policy keeps every quantitative metric within limits: the summary
	// reports status=ok and the self-test driver exits 0, while each case's
	// manifest preserves its own exit_code=2 as the damage signal.
	hardFailure := false
	for _, r := range results {
		if r.exitCode == 1 {
			hardFailure = true
		}
	}
	status := "ok"
	if hardFailure {
		status = "failed"
	}
	fmt.Printf("status=%s frames=%d sync_error_ms<=%.1f recovered_rate>=%.2f p95_latency_ms<=%.1f manifest_sha256=%s\n",
		status, *frames, validate.DefaultThresholds().SyncErrorMs,
		validate.DefaultThresholds().RecoveredRate,
		validate.DefaultThresholds().P95LatencyMs, primary)
	if hardFailure {
		os.Exit(1)
	}
	os.Exit(0)
}
