// Command medpipe is the terminal adapter for the regional edge media pipeline.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"medpipe/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("medpipe", flag.ContinueOnError)
	fs.SetOutput(stderr)

	selftest := fs.Bool("selftest", false, "generate fixtures and run the full deterministic self-test")
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	budgetMS := fs.Int64("budget-ms", 5000, "processing-time budget in milliseconds")
	maxBytes := fs.Int64("max-bytes", 1<<20, "maximum delivery size in bytes")
	minQuality := fs.Float64("min-quality", 99, "minimum quality/consistency percentage")
	outDir := fs.String("out-dir", "out/selftest", "directory for PPM frames, metadata and manifests")
	checkpoint := fs.String("checkpoint", "", "checkpoint path; defaults to <out-dir>/checkpoint.gob")
	resume := fs.String("resume", "", "resume from the named pipeline checkpoint")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected positional arguments: %v\n", fs.Args())
		return 2
	}
	if !*selftest && *resume == "" {
		fmt.Fprintln(stderr, "either --selftest or --resume=<checkpoint> is required")
		return 2
	}
	if *budgetMS < 0 || *maxBytes < 0 || *seed < 0 {
		fmt.Fprintln(stderr, "--seed, --budget-ms and --max-bytes must be non-negative")
		return 2
	}
	if *minQuality < 0 || *minQuality > 100 {
		fmt.Fprintln(stderr, "--min-quality must be between 0 and 100")
		return 2
	}

	cp := *checkpoint
	if cp == "" && *outDir != "" {
		cp = filepath.Join(*outDir, "checkpoint.gob")
	}

	var res *pipeline.RunResult
	var err error
	if *resume != "" {
		res, err = pipeline.Resume(*resume)
	} else {
		cfg := pipeline.Config{
			Seed: *seed, BudgetMS: *budgetMS, MaxBytes: *maxBytes,
			MinQuality: *minQuality, OutDir: *outDir, CheckpointPath: cp,
		}
		res, err = pipeline.Run(cfg)
	}
	if err != nil {
		fmt.Fprintf(stderr, "medpipe: %v\n", err)
		return 1
	}
	if res == nil || res.Summary.SegmentSHA256 == "" {
		fmt.Fprintln(stderr, "medpipe: pipeline returned no verification summary")
		return 1
	}
	fmt.Fprintln(stdout, res.Summary.Line())
	return 0
}
