// Command mediasync is the terminal entry point for the offline media
// processing kernel. It only generates fixtures, invokes the library
// subsystems and prints results: all parsing, buffering, recovery, sync and
// verification logic lives in internal packages.
//
// Exit codes: 0 all assertions passed, 1 an acceptance assertion failed,
// 2 invalid arguments or input.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"mediasync/internal/dash"
	"mediasync/internal/hls"
	"mediasync/internal/model"
	"mediasync/internal/pipeline"
	"mediasync/internal/sample"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("mediasync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	selfTest := fs.Bool("self-test", false, "generate fixtures and run the full acceptance kernel")
	seed := fs.Int64("seed", 0, "deterministic fixture seed")
	loss := fs.Int("loss", 0, "simulated RTP packet loss percentage (0-100)")
	jitter := fs.Int("jitter-ms", 0, "simulated jitter/reorder window in packets")
	budget := fs.Int64("budget-ms", 0, "processing budget in virtual milliseconds")
	maxBytes := fs.Int64("max-bytes", 0, "delivery size cap in bytes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*selfTest {
		fmt.Fprintln(os.Stderr, "mediasync: only --self-test mode is supported in this offline build")
		return 2
	}
	if *loss < 0 || *loss > 100 || *jitter < 0 || *budget < 0 || *maxBytes < 0 {
		fmt.Fprintln(os.Stderr, "mediasync: loss must be 0-100 and jitter/budget/max-bytes must be non-negative")
		return 2
	}

	// 1. Illegal-timescale DASH fixture must be rejected with a located error.
	dashManifest := dash.Parse(sample.IllegalTimescaleManifest())
	if len(dashManifest.Errors) == 0 ||
		model.Classify(dashManifest.Errors[0]) != model.KindInvalidTimescale {
		fmt.Fprintf(os.Stderr, "ASSERT FAIL: illegal timescale not rejected: %+v\n", dashManifest.Errors)
		return 1
	}

	// 2. HLS discontinuity fixture must advance periods without stitching.
	playlist, err := hls.Parse(sample.DiscontinuityPlaylist(), 90000)
	if err != nil || playlist.DiscoCount != 2 {
		fmt.Fprintf(os.Stderr, "ASSERT FAIL: HLS discontinuity parse failed: %v %+v\n", err, playlist)
		return 1
	}

	// 3. Full kernel over the deterministic multi-representation fixtures.
	lanes := sample.BuildAll(*seed, sample.Params{LossPct: *loss, JitterMS: *jitter})
	report := pipeline.Run(pipeline.Config{
		Seed: *seed, LossPct: *loss, JitterMS: *jitter,
		BudgetMS: *budget, MaxBytes: *maxBytes,
	}, lanes)

	printReport(report, dashManifest.Errors, playlist)

	// 4. Acceptance assertions.
	failed := false
	if report.RoundTripRate != 1.0 {
		fmt.Fprintf(os.Stderr, "ASSERT FAIL: roundtrip_rate=%.3f, want 1.000 for healthy samples\n",
			report.RoundTripRate)
		failed = true
	}
	// The deliberately corrupt sample must be located (truncation) and must
	// never masquerade as a healthy round-trip failure.
	if report.CorruptIsolated == 0 || !onlyCorruptLaneFailed(report) {
		fmt.Fprintln(os.Stderr, "ASSERT FAIL: corrupt sample not located/isolated correctly")
		failed = true
	}
	if report.KeyframeHitRate != 1.0 {
		fmt.Fprintf(os.Stderr, "ASSERT FAIL: keyframe_hit_rate=%.3f, want 1.000\n",
			report.KeyframeHitRate)
		failed = true
	}
	if !coreLanesComplete(report) {
		fmt.Fprintln(os.Stderr, "ASSERT FAIL: core dialogue/subtitle lanes are not complete")
		failed = true
	}
	if failed {
		return 1
	}
	fmt.Println("ASSERT: all acceptance checks passed")
	return 0
}

func printReport(r *pipeline.Report, dashErrors []*model.MediaError, playlist *hls.Playlist) {
	fmt.Printf("roundtrip_rate: %.3f\n", r.RoundTripRate)
	fmt.Printf("keyframe_hit_rate: %.3f\n", r.KeyframeHitRate)
	fmt.Printf("corrupt_samples_isolated: %d\n", r.CorruptIsolated)

	fmt.Println("segment_hashes:")
	for _, h := range r.SegmentHashes {
		fmt.Printf("  %s/p%d/i%d %s\n", h.Representation, h.PeriodID, h.Index, h.SHA256)
	}

	fmt.Printf("recovered: %d\n", r.Counters.Recovered)
	fmt.Printf("lost: %d\n", r.Counters.Lost)
	fmt.Printf("duplicate: %d\n", r.Counters.Duplicate)
	fmt.Printf("late: %d\n", r.Counters.Late)
	fmt.Printf("out_of_order: %d\n", r.Counters.OutOfOrder)
	fmt.Printf("evicted: %d\n", r.Counters.Evicted)
	fmt.Printf("boundary_errors: %d\n", r.Counters.BoundaryErrors)
	fmt.Printf("hls_discontinuities: %d\n", playlist.DiscoCount)
	fmt.Printf("invalid_timescale_rejected: %s @line %d\n",
		dashErrors[0].Kind, dashErrors[0].Offset)

	fmt.Println("budget_decisions:")
	for _, d := range r.BudgetDecisions {
		fmt.Printf("  %s priority=%d action=%s cost_ms=%d size_bytes=%d reason=%q\n",
			d.Representation, d.Priority, d.Action, d.CostMS, d.SizeBytes, d.Reason)
	}
	fmt.Printf("budget_used_ms: %d\nsize_used_bytes: %d\n", r.BudgetUsedMS, r.SizeUsedBytes)

	// Error classifications are printed in deterministic, de-duplicated order.
	kinds := map[string]int{}
	for _, e := range r.Errors {
		kinds[string(e.Kind)]++
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("error_classifications:")
	for _, k := range keys {
		fmt.Printf("  %s: %d\n", k, kinds[k])
	}
}

// onlyCorruptLaneFailed verifies every round-trip error belongs to the
// deliberately corrupt representation and is a truncation classification.
func onlyCorruptLaneFailed(r *pipeline.Report) bool {
	for _, e := range r.Errors {
		if e.Subsystem != "verify" {
			continue
		}
		if model.Classify(e) != model.KindTruncatedMP4 ||
			!strings.Contains(e.Context, "sfx-corrupt") {
			return false
		}
	}
	return true
}

// coreLanesComplete guarantees the dialogue and subtitle representations were
// never suspended and still contribute aligned segments.
func coreLanesComplete(r *pipeline.Report) bool {
	keptCore := map[string]bool{}
	for _, d := range r.BudgetDecisions {
		if d.Priority == model.PriorityCore {
			if d.Action != "kept" {
				return false
			}
			keptCore[d.Representation] = false
		}
	}
	if len(keptCore) < 2 {
		return false
	}
	for _, s := range r.AlignedSegments {
		if _, ok := keptCore[s.Representation]; ok {
			keptCore[s.Representation] = true
		}
	}
	for _, present := range keptCore {
		if !present {
			return false
		}
	}
	return true
}
