// Command mediapipeline is the terminal acceptance entry point for the media
// processing library.  It generates local sample media, runs the full pipeline
// and prints a stable, machine-readable summary.  Exit codes follow the
// contract: 0 validation passed, 2 damaged input fully reported, 1 budget or
// output validation failure.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/demo"
	"mediapipeline/internal/ingest"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	if args[0] != "demo" {
		fmt.Fprintf(os.Stderr, "unknown command %q (expected \"demo\")\n", args[0])
		usage()
		return 1
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	codecName := fs.String("codec", "av1", "codec: h264|h265|av1")
	seed := fs.Int64("seed", 17, "deterministic sample seed")
	bandwidth := fs.Int64("bandwidth", 0, "override normal bandwidth in bytes/second")
	inject := fs.String("inject", "", "comma-separated fault injections")
	interruptAfter := fs.Int("interrupt-after", 0, "stop after N newly committed units")
	resume := fs.Bool("resume", false, "resume from the persisted checkpoint")
	stateDir := fs.String("state-dir", "", "checkpoint/staging directory (default temp)")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}

	codec, err := parseCodec(*codecName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir := *stateDir
	if dir == "" {
		dir, err = os.MkdirTemp("", "mediapipeline-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	} else if !*resume {
		// A fresh, non-resuming run starts from an empty state directory so no
		// stale checkpoint can influence the result.
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := os.MkdirAll(filepath.Clean(dir), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	cfg := demo.Config{
		Codec:          codec,
		Seed:           *seed,
		BandwidthBps:   *bandwidth,
		Injections:     demo.ParseInjections(*inject),
		InterruptAfter: *interruptAfter,
		Resume:         *resume,
		StateDir:       dir,
	}
	ctx := context.Background()

	// First phase: an interrupting run stops deliberately after N units.
	if *interruptAfter > 0 && !*resume {
		first, err := demo.Run(ctx, cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		demo.WriteSummary(os.Stdout, first)
		if !first.InterruptedRun {
			return first.ExitCode()
		}
		// Second phase: re-enter with the same config and --resume.
		cfg.Resume = true
		cfg.InterruptAfter = 0
		res, err := demo.Run(ctx, cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		demo.WriteSummary(os.Stdout, res)
		return res.ExitCode()
	}

	res, err := demo.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	demo.WriteSummary(os.Stdout, res)
	return res.ExitCode()
}

func parseCodec(name string) (ingest.Codec, error) {
	switch name {
	case "h264":
		return ingest.CodecH264, nil
	case "h265":
		return ingest.CodecH265, nil
	case "av1":
		return ingest.CodecAV1, nil
	default:
		return "", fmt.Errorf("unknown codec %q (want h264, h265 or av1)", name)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediapipeline demo [flags]")
	fmt.Fprintln(os.Stderr, "flags: --codec --seed --bandwidth --inject --interrupt-after --resume --state-dir")
}
