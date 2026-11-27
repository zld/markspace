// Command continuity is the terminal entry point for the local media
// continuity kernel. It generates deterministic RTP/MP4 fixtures, runs the
// resumable scan/damage/keyframe/manifest pipeline and prints a summary.
//
// Exit codes: 0 = complete, clean input; 2 = damaged input, report
// generated; 1 = configuration, dependency or resume-state error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"continuity/internal/pipeline"
)

const unifiedTimescale = 48000

var errInterrupt = errors.New("interrupt requested")

type config struct {
	Seed             int64
	Out              string
	InterruptAfter   string
	ReorderWindow    int
	AudioTarget      int64
	VideoTarget      int64
	MaxKeyframeTicks int64 // on the 90 kHz video clock
}

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: continuity demo --seed N --out DIR [--interrupt-after STEP]")
		return 1
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	cfg := config{}
	fs.Int64Var(&cfg.Seed, "seed", 17, "fixture generation seed")
	fs.StringVar(&cfg.Out, "out", "", "output directory (required)")
	fs.StringVar(&cfg.InterruptAfter, "interrupt-after", "", "stop after this step (simulate interruption)")
	fs.IntVar(&cfg.ReorderWindow, "reorder-window", 8, "RTP reorder window in packets")
	fs.Int64Var(&cfg.AudioTarget, "audio-bitrate", 128000, "target audio bitrate (bps)")
	fs.Int64Var(&cfg.VideoTarget, "video-bitrate", 800000, "target video bitrate (bps)")
	fs.Int64Var(&cfg.MaxKeyframeTicks, "max-keyframe-ticks", 135000, "max keyframe interval on the 90kHz video clock")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return 1
	}
	if cfg.Out == "" {
		fmt.Fprintln(os.Stderr, "continuity: --out is required")
		return 1
	}
	return runDemo(cfg)
}

func runDemo(cfg config) int {
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "continuity:", err)
		return 1
	}

	inputHash, err := inputDigest(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "continuity:", err)
		return 1
	}
	configHash := configDigest(cfg)

	p, err := buildPipeline(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "continuity:", err)
		return 1
	}
	if cfg.InterruptAfter != "" {
		found := false
		for _, id := range p.Order() {
			if id == cfg.InterruptAfter {
				found = true
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "continuity: unknown step %q for --interrupt-after\n", cfg.InterruptAfter)
			return 1
		}
		p.OnStepComplete = func(id string) error {
			if id == cfg.InterruptAfter {
				return errInterrupt
			}
			return nil
		}
	}

	cp, err := pipeline.LoadCheckpoint(cfg.Out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "continuity:", err)
		return 1
	}
	if err := cp.Bind(inputHash, configHash, p.Versions()); err != nil {
		fmt.Fprintln(os.Stderr, "continuity:", err)
		return 1
	}

	results, runErr := p.Run(pipeline.NewContext(), cp)
	if errors.Is(runErr, errInterrupt) {
		fmt.Printf("interrupted after step %q (simulated); checkpoint saved, rerun to resume\n", cfg.InterruptAfter)
		return 1
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "continuity:", runErr)
		return 1
	}
	for _, id := range p.Order() {
		r := results[id]
		switch r.Status {
		case pipeline.StatusFailed:
			fmt.Fprintf(os.Stderr, "continuity: step %s failed: %v\n", id, r.Err)
			return 1
		case pipeline.StatusSkipped:
			fmt.Fprintf(os.Stderr, "continuity: step %s skipped (dependency not successful)\n", id)
			return 1
		}
	}
	return printSummary(cfg, results)
}
