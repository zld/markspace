// Command mediad is the terminal acceptance entry point for the media
// processing kernel. It generates its own sample streams, runs the resumable
// pipeline and prints the contract metrics. There is no GUI and no network.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"mediad/internal/app"
	"mediad/internal/clock"
	"mediad/internal/sample"
	"mediad/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediad demo [--out DIR] [--fault truncate] [--interrupt-after NODE]")
		return 1
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "media-demo", "output directory")
	fault := fs.String("fault", "", "inject a fault: truncate")
	interruptAfter := fs.String("interrupt-after", "", "stop after this node, then resume")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if *fault != "" && *fault != "truncate" {
		fmt.Fprintf(os.Stderr, "unknown fault %q (supported: truncate)\n", *fault)
		return 1
	}

	if *fault == "truncate" {
		return runTruncate(*out)
	}
	if *interruptAfter != "" {
		return runInterruptResume(*out, *interruptAfter)
	}
	return runAllKinds(*out)
}

// runAllKinds runs every generated fixture and prints the acceptance metrics.
func runAllKinds(out string) int {
	for _, kind := range sample.AllKinds() {
		cfg := app.DefaultConfig(out + "/" + string(kind))
		rep, err := app.Run(kind, cfg)
		if err != nil {
			return classifyError(err)
		}
		printMetrics(string(kind), rep)
	}
	// The normal fixture must satisfy the strict acceptance envelope.
	normal, err := app.Run(sample.KindNormal, app.DefaultConfig(out+"/normal-verify"))
	if err != nil {
		return classifyError(err)
	}
	if err := verifyAcceptance(normal); err != nil {
		fmt.Fprintf(os.Stderr, "acceptance check failed: %v\n", err)
		return 1
	}
	fmt.Println("acceptance: OK (normal errors=0, alignment<=1ms, bitrate within 5%)")
	return pipeline.ExitOK
}

// runTruncate feeds a cut input and reports the exact truncation offset.
func runTruncate(out string) int {
	cfg := app.DefaultConfig(out)
	cfg.Fault = "truncate"
	rep, err := app.Run(sample.KindNormal, cfg)
	if err == nil {
		fmt.Fprintln(os.Stderr, "expected truncation failure but run completed")
		return 1
	}
	offset := rep.TruncationOffset()
	fmt.Printf("truncated_offset=%d\n", offset)
	fmt.Printf("diagnostics=%v\n", err)
	// No manifest is published for a corrupt input.
	if rep != nil && rep.Manifest != nil {
		fmt.Fprintln(os.Stderr, "corrupt run must not produce a manifest")
		return 1
	}
	return classifyError(err)
}

// runInterruptResume performs an interrupted run, then resumes in the same
// directory and proves the resumed manifest equals an uninterrupted run.
func runInterruptResume(out, node string) int {
	interrupted := app.DefaultConfig(out)
	interrupted.InterruptAfter = node
	partial, err := app.Run(sample.KindNormal, interrupted)
	if err != nil {
		return classifyError(err)
	}
	fmt.Printf("interrupted_after=%s ran=%d reused=%d completed=%v\n",
		node, partial.Ran(), partial.Reused(), partial.Completed())

	full, err := app.Run(sample.KindNormal, app.DefaultConfig(out+"/full-reference"))
	if err != nil {
		return classifyError(err)
	}
	resumed, err := app.Run(sample.KindNormal, app.DefaultConfig(out))
	if err != nil {
		return classifyError(err)
	}
	fmt.Printf("resume ran=%d reused=%d\n", resumed.Ran(), resumed.Reused())

	fullHash, _ := full.Manifest.DocumentHash()
	resumedHash, _ := resumed.Manifest.DocumentHash()
	if resumedHash != fullHash {
		fmt.Fprintf(os.Stderr, "resumed manifest %s != full run %s\n", resumedHash, fullHash)
		return 1
	}
	fmt.Printf("manifest_hash=%s\nresume_matches_full_run=true\n", resumedHash)
	return pipeline.ExitOK
}

func printMetrics(label string, rep *app.Report) {
	st := rep.Stats()
	fmt.Printf("fixture=%s\n", label)
	fmt.Printf("  bitrate_target_bps=%d\n", st.BitrateTargetBPS)
	fmt.Printf("  bitrate_actual_bps=%d\n", st.BitrateActualBPS)
	fmt.Printf("  quality_errors=%d\n", st.QualityErrors)
	fmt.Printf("  manifest_max_delta_ms=%d\n", st.ManifestMaxDelta)
	fmt.Printf("  duplicate_count=%d\n", st.DuplicateCount)
	fmt.Printf("  checkpoint_reused=%d\n", st.CheckpointReused)
}

// verifyAcceptance enforces the published tolerances on the normal fixture.
func verifyAcceptance(rep *app.Report) error {
	st := rep.Stats()
	if st.QualityErrors != 0 {
		return fmt.Errorf("normal fixture has %d quality errors", st.QualityErrors)
	}
	if st.ManifestMaxDelta > 1 {
		return fmt.Errorf("alignment delta %d ms exceeds 1 ms", st.ManifestMaxDelta)
	}
	if st.BitrateTargetBPS <= 0 {
		return fmt.Errorf("non-positive target bitrate")
	}
	dev := st.BitrateTargetBPS - st.BitrateActualBPS
	if dev < 0 {
		dev = -dev
	}
	// Within 5% of the target (the generated stream is constant cadence).
	limit := st.BitrateTargetBPS / 20
	if dev > limit {
		return fmt.Errorf("bitrate deviation %d exceeds 5%% limit %d", dev, limit)
	}
	// Every slice boundary must round-trip within one millisecond.
	for _, sl := range rep.Manifest.Slices {
		if clock.Ms(sl.EndTicks-sl.StartTicks)-clock.Ms(rep.Manifest.Slices[0].EndTicks) > 1 {
			return fmt.Errorf("slice duration drift exceeds 1 ms")
		}
	}
	return nil
}

// classifyError maps failures to the documented process exit codes.
func classifyError(err error) int {
	var blocked *pipeline.BlockedError
	if errors.As(err, &blocked) {
		fmt.Fprintf(os.Stderr, "dependency blocked: %v\n", err)
		return pipeline.ExitBlocked // 3
	}
	var re clock.RangeError
	if errors.As(err, &re) {
		fmt.Fprintf(os.Stderr, "corrupt input: %v\n", err)
		return pipeline.ExitCorruptInput // 2
	}
	fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
	return 1
}
