// Command mediaqc is the terminal acceptance entry point for the local media
// QC pipeline. In -selftest mode it generates seeded fixtures, runs the
// healthy/damaged/recovered paths and prints deterministic evidence.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mediaqc/internal/runner"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("mediaqc", flag.ContinueOnError)
	selftest := fs.Bool("selftest", false, "generate local fixtures and run all QC paths")
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	window := fs.Int("window", 8, "jitter buffer window size (packets)")
	toleranceMs := fs.Int64("tolerance-ms", 20, "A/V drift tolerance in milliseconds")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Global configuration validation => exit code 2.
	if !*selftest {
		fmt.Fprintln(os.Stderr, "mediaqc: only -selftest mode is supported in this build")
		return 2
	}
	if *window <= 0 {
		fmt.Fprintf(os.Stderr, "mediaqc: -window must be positive, got %d\n", *window)
		return 2
	}
	if *toleranceMs < 0 {
		fmt.Fprintf(os.Stderr, "mediaqc: -tolerance-ms must be non-negative, got %d\n", *toleranceMs)
		return 2
	}

	opts := runner.Options{Seed: *seed, Window: *window, ToleranceMs: *toleranceMs}
	report, code := runner.Run(opts)

	printHuman(report)
	printJSON(report)
	return code
}

func printHuman(r *runner.Report) {
	fmt.Println("=== mediaqc self-test evidence ===")
	fmt.Printf("seed=%d window=%d tolerance_ms=%d\n", r.Seed, r.Window, r.ToleranceMs)
	fmt.Println("--- paths ---")
	for _, p := range r.Paths {
		fmt.Printf("  %-18s %-16s status=%s manifest=%s\n", p.Name, p.MaterialID, p.Status, short(p.ManifestSHA))
		for _, e := range p.Errors {
			fmt.Printf("      error: %s\n", e)
		}
	}
	fmt.Println("--- metrics ---")
	fmt.Printf("av_sync_error_ms            %.3f\n", r.AVSyncErrorMs)
	fmt.Printf("packet_loss_recovery_rate   %.3f (single=%.3f double=%.3f)\n",
		r.PacketLossRecovery, r.SingleLossRate, r.DoubleLossRate)
	fmt.Printf("p95_latency_ms              %.3f\n", r.P95LatencyMs)
	fmt.Printf("unrecoverable_gaps          %d\n", r.UnrecoverableGaps)
	fmt.Printf("manifest_sha256             %s\n", r.ManifestSHA256)
	fmt.Printf("invariant_failures          %d\n", r.InvariantFailures)
	fmt.Printf("checkpoint_digest           %s\n", r.CheckpointDigest)
	fmt.Printf("determinism_ok              %t\n", r.DeterminismOK)
}

func printJSON(r *runner.Report) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediaqc: failed to render JSON: %v\n", err)
		return
	}
	fmt.Println("--- json ---")
	fmt.Println(string(b))
}

func short(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12] + "..."
}
