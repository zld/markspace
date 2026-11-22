// Command mediasync is the terminal adapter over the internal media pipeline.
// It parses run parameters, drives a fully local deterministic demo and maps
// stage outcomes onto process exit codes:
//
//	0 - all invariants passed
//	2 - damaged input or a track mapping error
//	3 - an internal processing or cancellation-propagation failure
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mediasync/internal/app"
	"mediasync/internal/demux"
	"mediasync/internal/ebml"
	"mediasync/internal/report"
)

const usage = `mediasync - local multi-track media synchronization and lossless remux

usage:
  mediasync demo [flags]

flags:`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 3
	}
	if args[0] != "demo" {
		fmt.Fprintf(os.Stderr, "unknown command %q (supported: demo)\n", args[0])
		return 3
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 41, "deterministic generator seed")
	trackList := fs.String("tracks", "video,audio,subtitle", "comma-separated track kinds")
	driftPPM := fs.Float64("drift-ppm", 180, "synthetic device drift in ppm")
	wrapAt := fs.Int64("wrap-at", 900000, "timestamp wrap modulus in ticks (0 disables)")
	loss := fs.Float64("loss", 0.02, "RTP packet loss rate in [0,0.5]")
	workers := fs.Int("workers", 4, "bounded worker count")
	queue := fs.Int("queue", 16, "bounded input queue depth")
	clusters := fs.Int("clusters", 6, "generated clusters")
	perCluster := fs.Int("blocks-per-cluster", 4, "generated blocks per cluster per track")
	if err := fs.Parse(args[1:]); err != nil {
		return 3
	}

	cfg := app.Config{
		Seed: *seed, Tracks: strings.Split(*trackList, ","),
		DriftPPM: *driftPPM, WrapAt: *wrapAt, Loss: *loss,
		Workers: *workers, Queue: *queue,
		Clusters: *clusters, PerCluster: *perCluster,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	workDir, err := os.MkdirTemp("", "mediasync-demo-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "workdir: %v\n", err)
		return 3
	}

	res, err := app.Run(ctx, cfg, workDir)
	if err != nil {
		return classifyError(err)
	}
	printResult(res)
	return 0
}

// classifyError maps domain failures onto the documented exit codes.
func classifyError(err error) int {
	var mapErr *demux.MapError
	if errors.As(err, &mapErr) {
		fmt.Fprintf(os.Stderr, "mapping error: %v\n", err)
		return 2
	}
	var ebmlErr *ebml.Error
	if errors.As(err, &ebmlErr) {
		fmt.Fprintf(os.Stderr, "input damage: %v\n", err)
		return 2
	}
	if errors.Is(err, report.ErrInvariant) || errors.Is(err, app.ErrInternal) {
		fmt.Fprintf(os.Stderr, "internal failure: %v\n", err)
		return 3
	}
	// Validation/config errors describe unusable input parameters.
	fmt.Fprintf(os.Stderr, "input error: %v\n", err)
	return 2
}

func printResult(res *app.Result) {
	m := res.Metrics
	fmt.Println("mediasync demo summary")
	fmt.Println("----------------------")
	fmt.Printf("sync_error_ms_p95:        %.2f\n", m.SyncErrorMSP95)
	fmt.Printf("packet_recovery_rate:    %.4f\n", m.PacketRecoveryRate)
	fmt.Printf("pipeline_latency_ms_p95: %.2f\n", m.PipelineLatencyMSP95)
	fmt.Printf("tracks_mapped:           %d\n", m.TracksMapped)
	fmt.Printf("damaged_cases:           %d\n", m.DamagedCases)
	fmt.Printf("rerun_digest_equal:      %t\n", m.RerunDigestEqual)
	fmt.Printf("recoverable_cases:       %d\n", m.RecoverableCases)
	fmt.Printf("rejected_writes:         %d\n", m.RejectedWrites)
	fmt.Printf("input_blocks:            %d\n", m.InputBlocks)
	fmt.Printf("output_samples:          %d\n", m.OutputSamples)
	fmt.Printf("webm_package:            %s\n", res.WebMPath)
	if res.MP4Path != "" {
		fmt.Printf("mp4_sidecar:             %s\n", res.MP4Path)
	}
	fmt.Println()
	fmt.Println("damage verdicts:")
	for _, v := range res.Verdicts {
		state := "REJECTED"
		if v.Recoverable {
			state = "RECOVERED"
		}
		fmt.Printf("  [%s] %-22s %s\n", state, v.Name, v.Reason)
	}
}
