package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `usage: rtp-rebuild [options]

Rebuild an RTP/RTCP media timeline and a client event log onto one 90 kHz
timeline, mux the reconstructed payloads into an MP4 track, validate the
result and write a deterministic manifest.

options:
  --self-test            generate fixtures, run the pipeline and print results
  --seed N               deterministic seed for generated fixtures (default 7)
  --budget-ms N          processing budget in milliseconds (default 10000)
  --out DIR              output directory (default /tmp/rtp-rebuild)
  --stream PATH          process an existing record stream instead of --self-test
  --resume               resume from the checkpoint in --out
  --interrupt-after NAME stop after NAME completes (ingest|reorder|rtcp|events|align|mux|validate)
  --help                 print this text
`

// Exit codes.
const (
	exitOK      = 0
	exitFail    = 1
	exitParam   = 2
	exitPartial = 3
)

// Stage names, in pipeline order.
var stageNames = []string{"ingest", "reorder", "rtcp", "events", "align", "mux", "validate"}

// Config is the parsed command line.
type Config struct {
	SelfTest       bool
	Seed           int64
	BudgetMS       int64
	OutDir         string
	StreamPath     string
	Resume         bool
	InterruptAfter string
	help           bool
}

func parseArgs(args []string) (*Config, error) {
	cfg := &Config{Seed: 7, BudgetMS: 10000, OutDir: "/tmp/rtp-rebuild"}
	fs := flag.NewFlagSet("rtp-rebuild", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&cfg.SelfTest, "self-test", false, "generate fixtures and run")
	fs.Int64Var(&cfg.Seed, "seed", cfg.Seed, "fixture seed")
	fs.Int64Var(&cfg.BudgetMS, "budget-ms", cfg.BudgetMS, "processing budget in ms")
	fs.StringVar(&cfg.OutDir, "out", cfg.OutDir, "output directory")
	fs.StringVar(&cfg.StreamPath, "stream", "", "existing record stream")
	fs.BoolVar(&cfg.Resume, "resume", false, "resume from checkpoint")
	fs.StringVar(&cfg.InterruptAfter, "interrupt-after", "", "stop after this stage")
	fs.BoolVar(&cfg.help, "help", false, "print usage")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			cfg.help = true
			return cfg, nil
		}
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Seed < 0 {
		return fmt.Errorf("--seed must be non-negative, got %d", c.Seed)
	}
	if c.BudgetMS <= 0 {
		return fmt.Errorf("--budget-ms must be positive, got %d", c.BudgetMS)
	}
	if c.OutDir == "" {
		return errors.New("--out must not be empty")
	}
	if !c.SelfTest && c.StreamPath == "" {
		return errors.New("one of --self-test or --stream is required")
	}
	if c.SelfTest && c.StreamPath != "" {
		return errors.New("--self-test and --stream are mutually exclusive")
	}
	if c.InterruptAfter != "" {
		valid := false
		for _, name := range stageNames {
			if name == c.InterruptAfter {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("--interrupt-after %q is not a stage (%s)",
				c.InterruptAfter, strings.Join(stageNames, "|"))
		}
	}
	if c.StreamPath != "" {
		if _, err := os.Stat(c.StreamPath); err != nil {
			return fmt.Errorf("--stream: %w", err)
		}
	}
	return nil
}
