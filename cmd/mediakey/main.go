// Command mediakey packages locally generated media segments into an
// encrypted asset, issues a local license and validates the result offline.
//
// It is a terminal-only tool: all behaviour is driven by flags and reported as
// stable JSON plus a key=value summary. Exit codes are 0 for a clean run, 2
// when any validation check failed and 1 for an argument or build error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"mediakey/internal/pipeline"
	"mediakey/internal/reason"
	"mediakey/internal/report"
	generator "mediakey/testdata"
)

const (
	exitOK    = 0
	exitError = 1
	exitCheck = 2

	maxReplayAttempts = 256
	maxRotateEvery    = generator.MaxSegments
)

// demoFlags is the parsed flag set for the demo subcommand.
type demoFlags struct {
	segments       int
	rotateEvery    int
	licenseTTL     time.Duration
	advanceClock   time.Duration
	replayCheck    bool
	corruptSegment int
	dropSegment    int
	seed           int64
	production     bool
	assetID        string
	segmentSize    int
	replayAttempts int
	jsonOnly       bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It never calls os.Exit itself.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "error: unknown subcommand %q\n\n", args[0])
		usage(stderr)
		return exitError
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `mediakey - offline media packaging and license validation

usage:
  mediakey demo [flags]

flags:
  --segments N          number of sample segments (1..10000, default 12)
  --rotate-every N      rotate the key every N segments (default 3)
  --license-ttl D       license validity duration, e.g. 60s (default 1m)
  --advance-clock D     advance the injected clock after validation to probe
                        license and key expiry (default 0s)
  --replay-check        present one license serial concurrently to probe replay
  --corrupt-segment N   flip a byte in segment N to provoke AUTH_FAILED
  --drop-segment N      withhold segment N to provoke SEGMENT_MISSING
  --seed N              deterministic seed for content, keys and nonces
  --production          use the system clock and crypto/rand instead of the
                        seeded demo clock and streams
  --asset-id ID         asset identifier (default demo-asset)
  --segment-size N      nominal plaintext size per segment (default 4096)
  --replay-attempts N   concurrent replay presentations (default 4)
  --json                print the JSON report only, without the summary

exit codes:
  0  validation passed
  2  validation reported failures
  1  argument or build error
`)
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	cfg, code, done := parseDemoFlags(args, stderr)
	if done {
		return code
	}

	segments, err := generator.Generate(generator.Options{
		Seed:         cfg.seed,
		SegmentCount: cfg.segments,
		SegmentSize:  cfg.segmentSize,
	})
	if err != nil {
		return fail(stdout, stderr, reason.INVALID_INPUT, err)
	}

	runCfg := cfg.pipelineConfig(segments)

	rep, err := (&pipeline.Runner{}).Run(runCfg)
	if err != nil {
		var pkgErr *pipeline.PackageError
		if errors.As(err, &pkgErr) {
			return fail(stdout, stderr, pkgErr.Code, err)
		}
		return fail(stdout, stderr, reason.BUILD_ERROR, err)
	}

	if !cfg.jsonOnly {
		if err := rep.WriteSummary(stdout); err != nil {
			fmt.Fprintf(stderr, "error: write summary: %v\n", err)
			return exitError
		}
	}
	if err := rep.WriteJSON(stdout); err != nil {
		fmt.Fprintf(stderr, "error: write report: %v\n", err)
		return exitError
	}
	return rep.ExitCode()
}

// fail writes a machine-readable error envelope plus a human message.
func fail(stdout, stderr io.Writer, code reason.Code, err error) int {
	fmt.Fprintf(stderr, "error: %s: %v\n", code, err)
	if werr := report.WriteErrorJSON(stdout, code, err.Error()); werr != nil {
		fmt.Fprintf(stderr, "error: write failure report: %v\n", werr)
	}
	return exitError
}

// parseDemoFlags returns the configuration, an exit code and whether the run
// should stop immediately (on help, a parse error or invalid values).
func parseDemoFlags(args []string, stderr io.Writer) (demoFlags, int, bool) {
	cfg := demoFlags{
		segments:       12,
		rotateEvery:    3,
		licenseTTL:     time.Minute,
		corruptSegment: -1,
		dropSegment:    -1,
		seed:           generator.DefaultSeed,
		assetID:        "demo-asset",
		segmentSize:    generator.DefaultSegSize,
		replayAttempts: 4,
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&cfg.segments, "segments", cfg.segments, "number of sample segments")
	fs.IntVar(&cfg.rotateEvery, "rotate-every", cfg.rotateEvery, "rotate the key every N segments")
	fs.DurationVar(&cfg.licenseTTL, "license-ttl", cfg.licenseTTL, "license validity duration")
	fs.DurationVar(&cfg.advanceClock, "advance-clock", 0, "advance the injected clock after validation")
	fs.BoolVar(&cfg.replayCheck, "replay-check", false, "probe concurrent replay of one serial")
	fs.IntVar(&cfg.corruptSegment, "corrupt-segment", cfg.corruptSegment, "flip a byte in segment N")
	fs.IntVar(&cfg.dropSegment, "drop-segment", cfg.dropSegment, "withhold segment N")
	fs.Int64Var(&cfg.seed, "seed", cfg.seed, "deterministic seed for demo runs")
	fs.BoolVar(&cfg.production, "production", false, "use the system clock and crypto/rand")
	fs.StringVar(&cfg.assetID, "asset-id", cfg.assetID, "asset identifier")
	fs.IntVar(&cfg.segmentSize, "segment-size", cfg.segmentSize, "nominal plaintext size per segment")
	fs.IntVar(&cfg.replayAttempts, "replay-attempts", cfg.replayAttempts, "concurrent replay presentations")
	fs.BoolVar(&cfg.jsonOnly, "json", false, "print JSON only")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, exitOK, true
		}
		return cfg, exitError, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected arguments: %v\n", fs.Args())
		return cfg, exitError, true
	}
	if err := cfg.validate(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return cfg, exitError, true
	}
	return cfg, exitOK, false
}

// pipelineConfig assembles the run configuration. Seeded mode is the demo
// default: it uses a fixed clock anchor and deterministic streams so rotation
// and expiry boundaries reproduce on every machine. --production switches to
// the system clock and crypto/rand, as a deployment would run.
func (c demoFlags) pipelineConfig(segments []generator.Segment) pipeline.Config {
	cfg := pipeline.Config{
		AssetID:        c.assetID,
		Segments:       segments,
		RotateEvery:    c.rotateEvery,
		LicenseTTL:     c.licenseTTL,
		AdvanceClock:   c.advanceClock,
		CorruptSegment: c.corruptSegment,
		DropSegment:    c.dropSegment,
		ReplayCheck:    c.replayCheck,
		ReplayAttempts: c.replayAttempts,
	}
	if c.production {
		cfg.Clock = pipeline.SystemClock()
		return cfg
	}
	cfg.Clock = pipeline.NewClock(generator.BaseTime)
	cfg.KeyRandom = generator.NewDeterministicStream(c.seed, "keys")
	cfg.NonceRandom = generator.NewDeterministicStream(c.seed, "nonces")
	cfg.SecretRandom = generator.NewDeterministicStream(c.seed, "license-secret")
	return cfg
}

func (c demoFlags) validate() error {
	switch {
	case c.segments < 1 || c.segments > generator.MaxSegments:
		return fmt.Errorf("segments must be between 1 and %d, got %d", generator.MaxSegments, c.segments)
	case c.rotateEvery < 1 || c.rotateEvery > maxRotateEvery:
		return fmt.Errorf("rotate-every must be between 1 and %d, got %d", maxRotateEvery, c.rotateEvery)
	case c.licenseTTL <= 0:
		return fmt.Errorf("license-ttl must be positive, got %s", c.licenseTTL)
	case c.advanceClock < 0:
		return fmt.Errorf("advance-clock must not be negative, got %s", c.advanceClock)
	case c.segmentSize < 1:
		return fmt.Errorf("segment-size must be positive, got %d", c.segmentSize)
	case c.replayAttempts < 1 || c.replayAttempts > maxReplayAttempts:
		return fmt.Errorf("replay-attempts must be between 1 and %d, got %d", maxReplayAttempts, c.replayAttempts)
	case c.assetID == "":
		return errors.New("asset-id must not be empty")
	case c.corruptSegment < -1 || c.corruptSegment >= c.segments:
		return fmt.Errorf("corrupt-segment must be within [0,%d), got %d", c.segments, c.corruptSegment)
	case c.dropSegment < -1 || c.dropSegment >= c.segments:
		return fmt.Errorf("drop-segment must be within [0,%d), got %d", c.segments, c.dropSegment)
	case c.corruptSegment >= 0 && c.corruptSegment == c.dropSegment:
		return errors.New("corrupt-segment and drop-segment must differ")
	}
	return nil
}
