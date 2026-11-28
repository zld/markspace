package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"medialab/internal/fixtures"
	"medialab/internal/model"
	"medialab/internal/pipeline"
)

// Exit codes form part of the CLI contract.
const (
	exitOK       = 0
	exitHard     = 1
	exitVerify   = 2
	exitInternal = 3
)

// degenerateBudgetForConflict is the processing budget the budget-conflict
// sample uses. It is too small for any candidate, which is the point of the
// case: the planner must refuse rather than silently degrade.
const degenerateBudgetForConflict = 1

// degenerateBytesForConflict is the byte budget the budget-conflict sample
// clamps to; no rendered frame can fit in it.
const degenerateBytesForConflict = 64

// cacheBoundaryCap is the cache cap the cache-boundary sample clamps to. It is
// larger than one 128x96 job but smaller than two, so the pool is forced to
// serialise and the printed peak proves the cap held.
const cacheBoundaryCap = 100_000

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return exitHard
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "cases":
		for _, c := range fixtures.Cases {
			fmt.Fprintln(stdout, c)
		}
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return exitHard
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, `medialab - deterministic local media processing lab

Usage:
  medialab demo [flags]      generate a case, process it and verify artefacts
  medialab cases             list generated sample cases
  medialab help              show this message

Demo flags:
  --case NAME        normal | corrupt-hash | reorder-loss |
                     timestamp-regression | budget-conflict | cache-boundary
  --seed N           fixture seed (default 7); same seed gives same hashes
  --budget-ms N      processing budget in milliseconds (default 500)
  --max-bytes N      artefact byte budget (default 1048576)
  --workers N        worker concurrency (default 2)
  --cache-bytes N    peak cache byte cap (default 25165824)
  --reorder-window N jitter window in packets (default 8)
  --max-dim N        maximum frame side in pixels (default 4096)
  --min-quality Q    fidelity floor in (0,1] (default 0.5)
  --out DIR          keep input and artefacts under DIR
  --keep-input       retain the generated input directory

Exit status: 0 success or recoverable warnings only, 1 input hard error,
2 verification failure.`)
}

func runDemo(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg pipeline.Config
	fs.StringVar(&cfg.Case, "case", fixtures.CaseNormal, "sample case")
	fs.Int64Var(&cfg.Seed, "seed", 7, "fixture seed")
	fs.Int64Var(&cfg.BudgetMS, "budget-ms", 500, "processing budget in milliseconds")
	fs.Int64Var(&cfg.MaxBytes, "max-bytes", 1<<20, "artefact byte budget")
	fs.IntVar(&cfg.Workers, "workers", 2, "worker concurrency")
	fs.Int64Var(&cfg.CacheBytes, "cache-bytes", 24<<20, "peak cache byte cap")
	fs.IntVar(&cfg.ReorderWin, "reorder-window", 8, "RTP jitter window in packets")
	fs.IntVar(&cfg.MaxDim, "max-dim", model.MaxDimension, "maximum frame side")
	fs.Float64Var(&cfg.MinQuality, "min-quality", 0.5, "pixel fidelity floor")
	fs.StringVar(&cfg.OutDir, "out", "", "output directory")
	fs.BoolVar(&cfg.KeepInput, "keep-input", false, "retain the input fixture")
	if err := fs.Parse(args); err != nil {
		return exitHard
	}
	if !knownCase(cfg.Case) {
		fmt.Fprintf(stderr, "unknown case %q; run 'medialab cases' for the list\n", cfg.Case)
		return exitHard
	}
	if cfg.Seed < 0 {
		fmt.Fprintln(stderr, "--seed must be non-negative")
		return exitHard
	}
	if cfg.BudgetMS <= 0 || cfg.MaxBytes <= 0 || cfg.Workers <= 0 || cfg.CacheBytes <= 0 {
		fmt.Fprintln(stderr, "budgets, workers and cache cap must be positive")
		return exitHard
	}
	if cfg.Case == fixtures.CaseBudgetConflict {
		// The sample's whole purpose is an unsatisfiable budget; clamp both so
		// the conflict is unambiguous, and say so rather than quietly ignoring
		// the caller's flags.
		if cfg.BudgetMS > degenerateBudgetForConflict {
			cfg.BudgetMS = degenerateBudgetForConflict
		}
		if cfg.MaxBytes > degenerateBytesForConflict {
			cfg.MaxBytes = degenerateBytesForConflict
		}
	}
	if cfg.Case == fixtures.CaseCacheBoundary && cfg.CacheBytes > cacheBoundaryCap {
		// Same reasoning: the sample exists to show the cap in action, so the
		// default must be small enough that jobs have to wait.
		cfg.CacheBytes = cacheBoundaryCap
	}

	res, err := pipeline.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(stderr, "medialab: %v\n", err)
		return exitInternal
	}
	printReport(stdout, res)
	switch {
	case res.ExitCode == exitVerify:
		fmt.Fprintln(stderr, "medialab: verification failed (exit 2)")
		return exitVerify
	case res.ExitCode != exitOK:
		fmt.Fprintln(stderr, "medialab: input hard error (exit 1)")
		return exitHard
	}
	return exitOK
}

func knownCase(name string) bool {
	for _, c := range fixtures.Cases {
		if c == name {
			return true
		}
	}
	return false
}

func printReport(w *os.File, res *pipeline.Result) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	p("case:              %s", res.Config.Case)
	p("seed:              %d", res.Config.Seed)
	p("input:             %s", res.InputDir)
	p("output:            %s", res.OutDir)
	p("budget-ms:         %d", res.Config.BudgetMS)
	p("max-bytes:         %d", res.Config.MaxBytes)
	p("workers:           %d", res.Config.Workers)
	p("cache-bytes:       %d", res.Config.CacheBytes)
	p("reorder-window:    %d", res.Config.ReorderWin)
	p("max-dim:           %d", res.Config.MaxDim)
	p("min-quality:       %.3f", res.Config.MinQuality)
	p("")

	if res.Plan != nil && res.Plan.Conflict != nil {
		p("plan:              NO FEASIBLE CANDIDATE")
		p("conflict reason:   %s", res.Plan.Conflict.Reason)
		p("conflict detail:   %s", res.Plan.Conflict.Detail)
		if res.Plan.Conflict.BestQuality > 0 {
			p("best quality:      %.3f (floor %.3f)", res.Plan.Conflict.BestQuality, res.Plan.Budget.MinQuality)
		} else {
			p("best quality:      none clears the %.3f floor", res.Plan.Budget.MinQuality)
		}
		p("grid bounds:       cheapest cost %d ms, smallest %d bytes",
			res.Plan.Conflict.BestCostMS, res.Plan.Conflict.SmallestBytes)
		p("candidate summary:")
		for _, c := range res.Plan.Conflict.Evaluated {
			p("  quality=%.3f step=%d fps=%d frames=%d bytes=%d costMs=%d",
				c.Quality, c.QuantStep, c.FPS, c.Frames, c.Bytes, c.CostMS)
		}
		p("")
	} else if res.Plan != nil && res.Plan.Chosen != nil {
		c := res.Plan.Chosen
		p("plan:              quality=%.3f step=%d fps=%d frames=%d bytes=%d costMs=%d",
			c.Quality, c.QuantStep, c.FPS, c.Frames, c.Bytes, c.CostMS)
		p("plan rejected:     %d candidate(s)", res.Plan.Rejected)
	}

	if res.Render != nil {
		m := res.Render.Manifest
		p("tracks:            %d", m.Summary.Tracks)
		p("frames:            %d (fill %d)", m.Summary.Frames, m.Summary.FillFrames)
		p("segments:          %d", len(m.Segments))
		p("")
		p("frame digests:")
		for _, f := range m.Frames {
			tag := ""
			if f.Fill {
				tag = " [fill]"
			}
			p("  track %d frame %d %s%s", f.Track, f.Sequence, f.SHA256, tag)
		}
		p("")
		p("segments:")
		for _, s := range m.Segments {
			p("  track %d %s samples=%d bytes=%d sha256=%s", s.Track, s.Path, s.Samples, s.Bytes, s.SHA256)
		}
		p("")
		p("pixel mean:        R=%.4f G=%.4f B=%.4f", m.Summary.MeanR, m.Summary.MeanG, m.Summary.MeanB)
		p("pixel variance:    %.6f", m.Summary.Variance)
		p("loudness peak:     %.2f dBFS", m.Summary.PeakLoudnessDBFS)
		p("max align error:   %d ns", res.Align.MaxErrorNS)
		p("artefact bytes:    %d", m.Summary.TotalBytes)
		p("peak cache bytes:  %d (configured %d)", res.Render.PeakCacheBytes, res.Config.CacheBytes)
		p("run hash:          %s", m.Summary.RunHash)
		p("")
	}
	if res.Verify != nil {
		v := res.Verify
		p("verified frames:   %d", v.Frames)
		p("verified bytes:    %d", v.TotalBytes)
		p("verified mean:     R=%.4f G=%.4f B=%.4f var=%.6f",
			v.MeanR, v.MeanG, v.MeanB, v.Variance)
		p("verified loudness: %.2f dBFS", v.PeakLoudness)
		p("verified run hash: %s", v.RunHash)
		p("")
	}
	if len(res.Warnings) > 0 {
		p("warnings (%d):", len(res.Warnings))
		for _, f := range res.Warnings {
			p("  %s", f.Error())
		}
	}
	if len(res.Faults) > 0 {
		p("faults (%d):", len(res.Faults))
		for _, f := range res.Faults {
			p("  %s", f.Error())
		}
	}
	if res.Dataset != nil && res.Dataset.Note != "" {
		p("case note:         %s", res.Dataset.Note)
	}
	p("processing time:   %s", res.Elapsed.Round(time.Microsecond))
	p("exit code:         %d", res.ExitCode)
}
