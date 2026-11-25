// Command mediad is the terminal adapter over the media processing kernel.
// It exposes a self-contained demo (fixture-generated inputs, no external
// files) and a cancellation demo. Exit codes follow the delivery contract:
// 0 analysis completed even if segments were rejected, 2 bad arguments,
// 1 internal failure or inability to emit the report.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"mediad/internal/fixture"
	"mediad/pipeline"
	"mediad/report"
)

const (
	exitOK       = 0
	exitInternal = 1
	exitArgs     = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return exitArgs
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "cancel":
		return runCancel(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		printUsage(os.Stderr)
		return exitArgs
	}
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `mediad - local media processing kernel

Usage:
  mediad demo   [--workers N] [--queue N] [--budget-ms N] [--max-bytes N]
  mediad cancel [--workers N] [--queue N]
  mediad help

All inputs are generated in-process; no external files are read.
`)
}

// commonFlags defines the shared bounded-concurrency and budget flags.
func commonFlags(fs *flag.FlagSet) *pipeline.Config {
	cfg := &pipeline.Config{}
	fs.IntVar(&cfg.Workers, "workers", 2, "maximum concurrent workers")
	fs.IntVar(&cfg.Queue, "queue", 2, "bounded task queue depth")
	fs.Uint64Var(&cfg.BudgetMs, "budget-ms", 500, "processing budget in milliseconds")
	fs.Int64Var(&cfg.MaxBytes, "max-bytes", 4096, "delivery byte-volume cap")
	return cfg
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitArgs
	}
	if code := validateCfg(cfg); code != exitOK {
		return code
	}

	out, err := pipeline.Run(context.Background(), fixture.Dataset(), *cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline failed: %v\n", err)
		return exitInternal
	}
	if err := report.Write(os.Stdout, out.Analysis, out.Plan, out.Merge); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write report: %v\n", err)
		return exitInternal
	}
	return exitOK
}

func runCancel(args []string) int {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := &pipeline.Config{}
	fs.IntVar(&cfg.Workers, "workers", 1, "maximum concurrent workers")
	fs.IntVar(&cfg.Queue, "queue", 2, "bounded task queue depth")
	if err := fs.Parse(args); err != nil {
		return exitArgs
	}
	if cfg.Workers < 1 || cfg.Queue < 0 {
		fmt.Fprintln(os.Stderr, "workers must be >= 1 and queue >= 0")
		return exitArgs
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	out, err := pipeline.Run(ctx, fixture.CancelDataset(), *cfg)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "expected cancellation, got: %v\n", err)
		return exitInternal
	}
	if elapsed > time.Second {
		fmt.Fprintf(os.Stderr, "cancellation took %v, exceeds 1s\n", elapsed)
		return exitInternal
	}
	// A canceled run still emits whatever was completed for review.
	if err := report.Write(os.Stdout, out.Analysis, out.Plan, out.Merge); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write report: %v\n", err)
		return exitInternal
	}
	fmt.Fprintf(os.Stderr, "canceled cleanly after %v\n", elapsed.Round(time.Millisecond))
	return exitOK
}

func validateCfg(cfg *pipeline.Config) int {
	if cfg.Workers < 1 {
		fmt.Fprintln(os.Stderr, "workers must be >= 1")
		return exitArgs
	}
	if cfg.Queue < 0 {
		fmt.Fprintln(os.Stderr, "queue must be >= 0")
		return exitArgs
	}
	if cfg.BudgetMs == 0 {
		fmt.Fprintln(os.Stderr, "budget-ms must be > 0")
		return exitArgs
	}
	if cfg.MaxBytes <= 0 {
		fmt.Fprintln(os.Stderr, "max-bytes must be > 0")
		return exitArgs
	}
	return exitOK
}
