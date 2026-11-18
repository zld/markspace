// Command jitterlab is a terminal-only receiver chain simulator for real
// time voice over impaired local UDP.
//
// Usage:
//
//	jitterlab replay [flags]   drive the state machine from a generated plan
//	jitterlab live   [flags]   send and receive over loopback UDP
//
// Exit codes: 0 success, 2 invalid parameters, 3 corrupted input or
// protocol, 4 output failure.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jitterlab/internal/buffer"
	"jitterlab/internal/metrics"
	"jitterlab/internal/receiver"
	"jitterlab/internal/report"
	"jitterlab/internal/testkit"
)

// Exit codes shared with the receiver package.
const (
	exitOK      = receiver.ExitOK
	exitUsage   = receiver.ExitUsage
	exitDamaged = receiver.ExitDamaged
	exitOutput  = receiver.ExitOutput
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run parses the subcommand and dispatches. It returns the process exit
// code and leaves error reporting to stderr.
func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: jitterlab <replay|live> [flags]")
		return exitUsage
	}
	switch args[0] {
	case "replay":
		return runReplay(args[1:], stderr)
	case "live":
		return runLive(args[1:], stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stderr, usageText)
		return exitOK
	default:
		fmt.Fprintf(stderr, "jitterlab: unknown command %q\n", args[0])
		return exitUsage
	}
}

const usageText = `jitterlab - loopback voice receiver chain simulator

  jitterlab replay [flags]   deterministic offline run from a generated plan
  jitterlab live   [flags]   real-time loopback UDP run

common flags:
  --frames N          number of voice frames (1..200000)
  --frame-ms MS       frame duration in ms (1..500)
  --target-ms MS      target buffer depth in ms
  --max-wait-ms MS    maximum playout wait in ms (>= target-ms)
  --window N          reorder window in frames (1..4096)
  --seed N            deterministic impairment seed
  --loss P            packet loss probability (0..1)
  --delay-ms MS       fixed added path latency
  --jitter-ms MS      maximum random added latency
  --burst-loss P      burst-loss start probability
  --burst-len N       frames lost per burst
  --reorder P         reorder probability (0..1)
  --duplicate P       duplicate probability (0..1)
  --max-invalid-run N consecutive corrupt packets before aborting (default 16)
  --out PATH          JSON Lines path, "-" or empty for stdout

replay-only flags:
  --burst-loss P --burst-len N --drift-ppm N
  --reorder-delay N --duplicate-delay N
  --corrupt P --truncate P --bad-version P

live-only flags:
  --duration D --read-timeout D --reorder-delay N --duplicate-delay N

exit codes: 0 ok, 2 bad parameters, 3 damaged input, 4 output failure
`

// commonFlags holds the parameters both subcommands share.
type commonFlags struct {
	frames        int
	frameMs       int
	targetMs      int
	maxWaitMs     int
	window        int
	seed          int64
	loss          float64
	reorder       float64
	duplicate     float64
	burst         float64
	burstLen      int
	drift         int
	delayMs       int
	jitterMs      int
	maxInvalidRun int
	out           string
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.IntVar(&c.frames, "frames", 1200, "number of voice frames")
	fs.IntVar(&c.frameMs, "frame-ms", 20, "frame duration in milliseconds")
	fs.IntVar(&c.targetMs, "target-ms", 80, "target buffer depth in milliseconds")
	fs.IntVar(&c.maxWaitMs, "max-wait-ms", 240, "maximum playout wait in milliseconds")
	fs.IntVar(&c.window, "window", 64, "reorder window in frames")
	fs.Int64Var(&c.seed, "seed", 0, "deterministic impairment seed")
	fs.Float64Var(&c.loss, "loss", 0, "packet loss probability")
	fs.Float64Var(&c.reorder, "reorder", 0, "reorder probability")
	fs.Float64Var(&c.duplicate, "duplicate", 0, "duplicate probability")
	fs.Float64Var(&c.burst, "burst-loss", 0, "per-frame burst-loss start probability")
	fs.IntVar(&c.burstLen, "burst-len", 0, "frames lost per burst")
	fs.IntVar(&c.drift, "drift-ppm", 0, "receiver clock drift in parts per million")
	fs.IntVar(&c.delayMs, "delay-ms", 0, "fixed added path latency in milliseconds")
	fs.IntVar(&c.jitterMs, "jitter-ms", 0, "maximum uniform added latency jitter in milliseconds")
	fs.IntVar(&c.maxInvalidRun, "max-invalid-run", receiver.DefaultMaxInvalidRun,
		"consecutive corrupt packets before aborting")
	fs.StringVar(&c.out, "out", "", `JSON Lines path ("-" or empty for stdout)`)
}

func (c *commonFlags) bufferConfig() buffer.Config {
	return buffer.Config{
		FrameMs:    c.frameMs,
		TargetMs:   c.targetMs,
		MaxWaitMs:  c.maxWaitMs,
		WindowSize: c.window,
	}
}

func (c *commonFlags) options() testkit.Options {
	return testkit.Options{
		Frames:       c.frames,
		FrameMs:      c.frameMs,
		Seed:         c.seed,
		Loss:         c.loss,
		Reorder:      c.reorder,
		Duplicate:    c.duplicate,
		BurstStart:   c.burst,
		BurstLen:     c.burstLen,
		DriftPPM:     c.drift,
		ExtraDelayMs: c.delayMs,
		JitterMs:     c.jitterMs,
	}
}

// parseFlags parses args, reporting flag.ErrHelp separately so --help can
// exit zero.
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return flag.ErrHelp
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

// validate checks every parameter that can be rejected before any work
// starts. Both buffer contradictions and impairment bounds live here.
func validate(c *commonFlags, opts testkit.Options) error {
	if c.maxInvalidRun <= 0 {
		return fmt.Errorf("--max-invalid-run must be positive")
	}
	if err := c.bufferConfig().Validate(); err != nil {
		return err
	}
	return opts.Validate()
}

// outcome is the common result shape both receive paths produce.
type outcome struct {
	frames   []metrics.Frame
	summary  metrics.Summary
	exitCode int
	err      error
}

// emit writes the report and returns the process exit code. A write or
// close failure is an output error (4); otherwise the receiver's own exit
// code wins so a damaged stream can be reported even with partial output.
func emit(out string, o outcome, stderr io.Writer, name string) int {
	w, closer, err := report.Create(out)
	if err != nil {
		fmt.Fprintf(stderr, "jitterlab %s: %v\n", name, err)
		return exitOutput
	}
	for _, f := range o.frames {
		if werr := w.WriteFrame(f); werr != nil {
			closer.Close()
			fmt.Fprintf(stderr, "jitterlab %s: write output: %v\n", name, werr)
			return exitOutput
		}
	}
	if werr := w.WriteSummary(o.summary); werr != nil {
		closer.Close()
		fmt.Fprintf(stderr, "jitterlab %s: write output: %v\n", name, werr)
		return exitOutput
	}
	if cerr := closer.Close(); cerr != nil {
		fmt.Fprintf(stderr, "jitterlab %s: close output: %v\n", name, cerr)
		return exitOutput
	}
	if o.err != nil {
		fmt.Fprintf(stderr, "jitterlab %s: %v\n", name, o.err)
		return o.exitCode
	}
	return exitOK
}

func runReplay(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	var (
		c        commonFlags
		reorderD int
		dupD     int
		corrupt  float64
		truncate float64
		badVer   float64
		decay    float64
	)
	c.register(fs)
	fs.IntVar(&reorderD, "reorder-delay", 3, "maximum reorder delay in frames")
	fs.IntVar(&dupD, "duplicate-delay", 3, "maximum duplicate lag in frames")
	fs.Float64Var(&corrupt, "corrupt", 0, "checksum corruption probability")
	fs.Float64Var(&truncate, "truncate", 0, "truncation probability")
	fs.Float64Var(&badVer, "bad-version", 0, "unknown-version probability")
	fs.Float64Var(&decay, "decay", 0.6, "concealment attenuation per slot")
	if err := parseFlags(fs, args, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintf(stderr, "jitterlab replay: %v\n", err)
		return exitUsage
	}

	opts := c.options()
	opts.ReorderDelay, opts.DuplicateDelay = reorderD, dupD
	opts.Corrupt, opts.Truncate, opts.BadVersion = corrupt, truncate, badVer
	if err := validate(&c, opts); err != nil {
		fmt.Fprintf(stderr, "jitterlab replay: %v\n", err)
		return exitUsage
	}

	// Open the output first so a bad path fails before any work is done.
	if err := probeOutput(c.out); err != nil {
		fmt.Fprintf(stderr, "jitterlab replay: %v\n", err)
		return exitOutput
	}

	res, err := receiver.Replay(receiver.ReplayConfig{
		Buffer:        c.bufferConfig(),
		Options:       opts,
		Decay:         decay,
		MaxInvalidRun: c.maxInvalidRun,
	})
	if err != nil && res.ExitCode == exitUsage {
		fmt.Fprintf(stderr, "jitterlab replay: %v\n", err)
		return exitUsage
	}
	return emit(c.out, outcome{
		frames:   res.Frames,
		summary:  res.Summary,
		exitCode: res.ExitCode,
		err:      err,
	}, stderr, "replay")
}

func runLive(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("live", flag.ContinueOnError)
	var (
		c        commonFlags
		duration time.Duration
		timeout  time.Duration
		decay    float64
		reorderD int
		dupD     int
	)
	c.register(fs)
	fs.DurationVar(&duration, "duration", 0, "hard run limit (0 = until the plan drains)")
	fs.DurationVar(&timeout, "read-timeout", 200*time.Millisecond, "socket read deadline")
	fs.IntVar(&reorderD, "reorder-delay", 3, "maximum reorder delay in frames")
	fs.IntVar(&dupD, "duplicate-delay", 3, "maximum duplicate lag in frames")
	fs.Float64Var(&decay, "decay", 0.6, "concealment attenuation per slot")
	if err := parseFlags(fs, args, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintf(stderr, "jitterlab live: %v\n", err)
		return exitUsage
	}
	opts := c.options()
	opts.ReorderDelay, opts.DuplicateDelay = reorderD, dupD
	if err := validate(&c, opts); err != nil {
		fmt.Fprintf(stderr, "jitterlab live: %v\n", err)
		return exitUsage
	}
	if timeout <= 0 {
		fmt.Fprintln(stderr, "jitterlab live: --read-timeout must be positive")
		return exitUsage
	}
	if duration < 0 {
		fmt.Fprintln(stderr, "jitterlab live: --duration must not be negative")
		return exitUsage
	}
	if err := probeOutput(c.out); err != nil {
		fmt.Fprintf(stderr, "jitterlab live: %v\n", err)
		return exitOutput
	}

	// Ctrl-C ends the run cleanly; the receiver still drains and reports.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := receiver.Live(ctx, receiver.LiveConfig{
		Buffer:        c.bufferConfig(),
		Options:       opts,
		Decay:         decay,
		MaxInvalidRun: c.maxInvalidRun,
		Duration:      duration,
		Timeout:       timeout,
	})
	if err != nil && res.ExitCode == exitUsage {
		fmt.Fprintf(stderr, "jitterlab live: %v\n", err)
		return exitUsage
	}
	return emit(c.out, outcome{
		frames:   res.Frames,
		summary:  res.Summary,
		exitCode: res.ExitCode,
		err:      err,
	}, stderr, "live")
}

// probeOutput checks that the destination can be opened for writing
// without truncating an existing file, so failures surface before a long
// run rather than after it.
func probeOutput(path string) error {
	if path == "" || path == "-" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	return f.Close()
}
