// Command avpipe is the terminal acceptance entry point for the recovery,
// parsing, alignment, DAG, manifest, and metrics pipeline.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/zld/avpipe/internal/fault"
	"github.com/zld/avpipe/internal/scenario"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		args = []string{"demo"}
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		printUsage(os.Stderr)
		return 1
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("avpipe demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	cfg := scenario.DefaultConfig()
	seed := fs.Int64("seed", cfg.Seed, "deterministic sample generator seed")
	loss := fs.Float64("loss", cfg.Loss, "per-packet random loss probability in [0,1]")
	truncate := fs.Int("truncate", 0, "truncate corruption fixture at this absolute byte offset (0 disables)")
	missingIndex := fs.Bool("missing-index", false, "omit sidx boxes and rebuild the index from fragments")
	segments := fs.Int("segments", cfg.Segments, "segments per representation")
	reps := fs.Int("representations", cfg.Representations, "number of bitrate representations")
	packets := fs.Int("packets", cfg.Packets, "self-generated RTP packet count")
	window := fs.Int("reorder-window", cfg.ReorderWindow, "RTP sequence reorder window")
	memoryMB := fs.Int("memory-mb", cfg.MemoryMB, "local memory budget in MiB")
	retries := fs.Int("retries", cfg.MaxRetries, "retry count after the initial attempt")
	maxRetries := fs.Int("max-retries", -1, "alias for --retries")
	out := fs.String("out", cfg.OutDir, "output directory for manifest.json")
	failNode := fs.String("fail-node", "", "optional DAG node to fail after retries (acceptance helper)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", fs.Args())
		return 1
	}

	retryCount := *retries
	if *maxRetries >= 0 {
		retryCount = *maxRetries
	}
	cfg = scenario.Config{
		Seed:            *seed,
		Loss:            *loss,
		Truncate:        *truncate,
		MissingIndex:    *missingIndex,
		Segments:        *segments,
		Representations: *reps,
		Packets:         *packets,
		ReorderWindow:   *window,
		MemoryMB:        *memoryMB,
		MaxRetries:      retryCount,
		OutDir:          *out,
		FailNode:        *failNode,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	result, err := scenario.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return fault.ExitCode(err)
	}
	result.Metrics.PrintKeyValues(os.Stdout)
	if result.ManifestPath != "" {
		fmt.Printf("manifest_path=%s\n", result.ManifestPath)
	}
	if result.DAG != nil && result.DAG.FirstFailedNode != "" {
		fmt.Printf("first_failed_node=%s\n", result.DAG.FirstFailedNode)
	}
	return result.Metrics.ExitCode
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "usage: avpipe demo [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  demo    generate local RTP/MP4/metadata samples and run recovery pipeline")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Exit codes: 0 ok, 1 arguments, 2 corrupt input isolated, 3 blocked dependency")
}
