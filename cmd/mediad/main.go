// Command mediad is the terminal acceptance entry point for the media
// kernel. It generates the declared local fixtures, runs them through the
// pipeline and prints one JSON object per line. Exit codes: 0 acceptance
// passed, 1 metrics missed, 2 bad parameters or input.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"mediad/internal/model"
	"mediad/internal/pipeline"
)

const (
	exitOK       = 0
	exitMetrics  = 1
	exitBadInput = 2
)

// thresholds are the normal-sample acceptance targets.
const (
	maxSyncErrorMs = 40
	minRecovery    = 0.80
	maxP95Latency  = 250
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitBadInput)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "fixtures":
		os.Exit(runFixtures(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(exitBadInput)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad - local media transport kernel acceptance tool

Usage:
  mediad demo     [flags]   run the normal scenario with overrides
  mediad fixtures [flags]   run every declared scenario
  mediad help

Flags for demo:
  --seed int            generator seed (default 42)
  --loss float          packet loss in [0,1] (default 0.05)
  --reorder-ms int      max reorder jitter (default 80)
  --late-window-ms int  late replay window (default 250)
  --report-every int    progress line interval, emitted samples (default 100)
  --fixture string      scenario name (default "normal")
  --cache int           retransmit cache size (default 512)

Flags for fixtures:
  --seed int            generator seed (default 42)
  --report-every int    progress line interval (default 0 = quiet)

Output is JSON lines; the last line is the run summary and carries
result_sha256. Identical seed and parameters produce an identical digest.
`)
}

type commonFlags struct {
	seed        int64
	loss        float64
	reorderMs   int64
	lateWindow  int64
	reportEvery int
	cache       int
	fixture     string
}

func parseCommon(fs *flag.FlagSet, args []string, withOverrides bool) (commonFlags, error) {
	var c commonFlags
	fs.Int64Var(&c.seed, "seed", 42, "generator seed")
	fs.Int64Var(&c.lateWindow, "late-window-ms", 250, "late replay window in ms")
	fs.IntVar(&c.reportEvery, "report-every", 100, "progress interval in emitted samples")
	fs.IntVar(&c.cache, "cache", 512, "retransmit cache capacity")
	if withOverrides {
		fs.Float64Var(&c.loss, "loss", 0.05, "packet loss in [0,1]")
		fs.Int64Var(&c.reorderMs, "reorder-ms", 80, "max reorder jitter in ms")
		fs.StringVar(&c.fixture, "fixture", "normal", "scenario name")
	} else {
		c.loss, c.reorderMs = -1, -1
	}
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if c.seed < 0 {
		return c, errors.New("--seed must be non-negative")
	}
	if c.cache < 1 {
		return c, errors.New("--cache must be positive")
	}
	if c.lateWindow < 0 {
		return c, errors.New("--late-window-ms must be non-negative")
	}
	if c.reportEvery < 0 {
		return c, errors.New("--report-every must be non-negative")
	}
	if withOverrides {
		if c.loss < 0 || c.loss > 1 {
			return c, fmt.Errorf("--loss %.4f outside [0,1]", c.loss)
		}
		if c.reorderMs < 0 {
			return c, errors.New("--reorder-ms must be non-negative")
		}
		if !knownFixture(c.fixture) {
			return c, fmt.Errorf("unknown fixture %q (have %v)", c.fixture, pipeline.FixtureNames())
		}
	}
	return c, nil
}

func knownFixture(name string) bool {
	for _, n := range pipeline.FixtureNames() {
		if n == name {
			return true
		}
	}
	return false
}

// newDemoFlagSet builds the demo flag set. It is split out so tests can
// exercise parseCommon without touching os.Args.
func newDemoFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func config(c commonFlags) pipeline.Config {
	return pipeline.Config{
		Seed:         c.seed,
		Fixture:      c.fixture,
		Loss:         c.loss,
		ReorderMs:    c.reorderMs,
		LateWindowMs: c.lateWindow,
		CacheSize:    c.cache,
		ReportEvery:  c.reportEvery,
	}
}

func progressLine(n int) {
	emit(map[string]any{"event": "progress", "emitted_samples": n})
}

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		return
	}
	fmt.Println(string(b))
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c, err := parseCommon(fs, args, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return exitBadInput
	}
	var onProgress func(int)
	if c.reportEvery > 0 {
		onProgress = progressLine
	}
	sum, err := pipeline.Run(config(c), onProgress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return exitBadInput
	}
	emit(sum)
	if fails := checkAcceptance(sum); len(fails) > 0 {
		for _, f := range fails {
			fmt.Fprintf(os.Stderr, "acceptance: %s\n", f)
		}
		return exitMetrics
	}
	return exitOK
}

func runFixtures(args []string) int {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c, err := parseCommon(fs, args, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixtures: %v\n", err)
		return exitBadInput
	}
	failed := 0
	for _, name := range pipeline.FixtureNames() {
		c.fixture = name
		sum, err := pipeline.Run(config(c), nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fixtures: %s: %v\n", name, err)
			return exitBadInput
		}
		emit(sum)
		// Only the normal sample carries the strict latency/recovery
		// targets; fault fixtures assert their own required signals.
		if name == "normal" {
			if fails := checkAcceptance(sum); len(fails) > 0 {
				failed++
				for _, f := range fails {
					fmt.Fprintf(os.Stderr, "acceptance[%s]: %s\n", name, f)
				}
			}
		}
		if fails := checkFixture(name, sum); len(fails) > 0 {
			failed++
			for _, f := range fails {
				fmt.Fprintf(os.Stderr, "acceptance[%s]: %s\n", name, f)
			}
		}
	}
	if failed > 0 {
		return exitMetrics
	}
	return exitOK
}

func checkAcceptance(s model.Summary) []string {
	var out []string
	if s.AVSyncErrorMs > maxSyncErrorMs {
		out = append(out, fmt.Sprintf("av_sync_error_ms=%d exceeds %d", s.AVSyncErrorMs, maxSyncErrorMs))
	}
	if s.RecoveryRate < minRecovery {
		out = append(out, fmt.Sprintf("recovery_rate=%.3f below %.2f", s.RecoveryRate, minRecovery))
	}
	if s.P95LatencyMs > maxP95Latency {
		out = append(out, fmt.Sprintf("p95_latency_ms=%d exceeds %d", s.P95LatencyMs, maxP95Latency))
	}
	if s.MalformedPackets > 0 {
		out = append(out, fmt.Sprintf("malformed_packets=%d in a clean sample", s.MalformedPackets))
	}
	return out
}

// checkFixture verifies the fault-specific signal each scenario exists to
// prove. The corrupt sample must surface parse damage downstream; the
// late-feedback sample must actually replay a recovered event.
func checkFixture(name string, s model.Summary) []string {
	var out []string
	require := func(ok bool, format string, args ...any) {
		if !ok {
			out = append(out, fmt.Sprintf(format, args...))
		}
	}
	switch name {
	case "corrupt":
		require(s.MalformedPackets > 0, "corrupt sample reported no malformed packets")
		require(s.BufferMalformed > 0, "corruption did not propagate to the buffer")
		require(s.SyncMalformed > 0, "corruption did not propagate to sync stats")
		require(s.EmittedSamples > 0, "valid packets after corruption were not emitted")
	case "truncated":
		require(s.TruncatedTail > 0, "truncated sample reported no truncated packet")
		require(s.MalformedPackets > 0, "truncated sample reported no malformed packets")
	case "late-rtcp":
		require(s.LateReplayed > 0, "late feedback produced no replay")
		require(s.LateFeedback > 0, "late feedback was not counted")
	case "no-sr":
		require(s.AVSyncConfidence == "low", "no-sr sample should report low confidence, got %q", s.AVSyncConfidence)
	case "wrap":
		require(s.QuarantinedPackets > 0, "wrap sample did not quarantine the ambiguous packet")
		require(s.DuplicatePackets >= 0, "duplicate accounting failed")
	case "clock-drift":
		require(s.AVSyncConfidence == "high", "clock-drift sample should keep high confidence")
		require(s.ClockDriftPPM != 0, "clock-drift sample reported no drift")
	}
	return out
}

// sortedNames is used by help text and tests; keep deterministic order.
