package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

type demoFlags struct {
	seed        int64
	workers     int
	queue       int
	out         string
	samplesPer  int
	cancelAfter time.Duration
	window      int
	dedupWindow int
	verbose     bool
}

func parseDemo(args []string) (*demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	d := &demoFlags{}
	fs.Int64Var(&d.seed, "seed", 41, "deterministic fixture seed")
	fs.IntVar(&d.workers, "workers", 4, "bounded worker pool size")
	fs.IntVar(&d.queue, "queue", 8, "bounded queue capacity")
	fs.StringVar(&d.out, "out", "", "output directory (required)")
	fs.IntVar(&d.samplesPer, "samples-per", 6, "samples per segment")
	fs.DurationVar(&d.cancelAfter, "cancel-after", 0, "cancel the pipeline after this delay (0 = never)")
	fs.IntVar(&d.window, "window", 4, "RTP reorder window in packets")
	fs.IntVar(&d.dedupWindow, "dedup-window", 64, "RTP duplicate filter window")
	fs.BoolVar(&d.verbose, "v", false, "verbose per-segment output")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if d.out == "" {
		return nil, errors.New("--out is required")
	}
	if d.workers < 1 || d.workers > 64 {
		return nil, fmt.Errorf("--workers %d outside 1..64", d.workers)
	}
	if d.queue < 1 || d.queue > 4096 {
		return nil, fmt.Errorf("--queue %d outside 1..4096", d.queue)
	}
	if d.samplesPer < 1 || d.samplesPer > 4096 {
		return nil, fmt.Errorf("--samples-per %d outside 1..4096", d.samplesPer)
	}
	if d.window < 1 || d.window > 4096 {
		return nil, fmt.Errorf("--window %d outside 1..4096", d.window)
	}
	return d, nil
}
