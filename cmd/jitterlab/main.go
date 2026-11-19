// jitterlab compares jitter-buffer playout behavior over two receive
// paths: deterministic offline replay and live loopback UDP.
//
// Exit codes: 0 clean finish, 2 invalid arguments, 3 unrecoverable
// runtime error. Individual bad packets only bump counters.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/receiver"
	"jitterlab/internal/report"
	"jitterlab/internal/scenario"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "replay":
		return runReplay(args[1:], stdout, stderr)
	case "listen":
		return runListen(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  jitterlab replay [flags]   deterministic offline network replay
  jitterlab listen  [flags]  receive live UDP datagrams (loopback)

replay flags: --frames --seed --loss-burst --reorder-depth --duplicate-rate
              --jitter-ms --drift-ppm [--json]
listen flags: --addr --duration [--json]
shared flags: --target-ms --min-target-ms --max-target-ms --window
              --sample-interval-ms --conceal-threshold --max-payload`)
}

// ctlFlags holds the shared controller tuning flags.
type ctlFlags struct {
	targetMs   float64
	minMs      float64
	maxMs      float64
	margin     float64
	window     int
	sampleMs   float64
	concealThr int
	maxPayload int
	jsonOut    bool
}

func addCtlFlags(fs *flag.FlagSet, c *ctlFlags) {
	fs.Float64Var(&c.targetMs, "target-ms", 60, "base target playout delay (ms)")
	fs.Float64Var(&c.minMs, "min-target-ms", 20, "adaptive target lower bound (ms)")
	fs.Float64Var(&c.maxMs, "max-target-ms", 400, "adaptive target upper bound (ms)")
	fs.Float64Var(&c.margin, "jitter-margin", 3, "multiplier on jitter estimate")
	fs.IntVar(&c.window, "window", 256, "max buffered packets per stream")
	fs.Float64Var(&c.sampleMs, "sample-interval-ms", 100, "watermark sampling period (ms)")
	fs.IntVar(&c.concealThr, "conceal-threshold", 6, "conceal run flagged unrecoverable")
	fs.IntVar(&c.maxPayload, "max-payload", 1200, "max accepted payload bytes")
	fs.BoolVar(&c.jsonOut, "json", false, "emit JSON report instead of text")
}

func (c *ctlFlags) validate() error {
	if c.minMs <= 0 || c.targetMs < c.minMs || c.targetMs > c.maxMs {
		return fmt.Errorf("need 0 < min-target-ms <= target-ms <= max-target-ms")
	}
	if c.window < 1 || c.window > 4096 {
		return fmt.Errorf("window must be in [1,4096]")
	}
	if c.sampleMs <= 0 || c.concealThr < 1 {
		return fmt.Errorf("sample-interval-ms and conceal-threshold must be positive")
	}
	if c.maxPayload < 1 {
		return fmt.Errorf("max-payload must be positive")
	}
	return nil
}

func (c *ctlFlags) controller() *jitter.Controller {
	return jitter.NewController(jitter.Config{
		FrameInterval:       20 * time.Millisecond,
		BaseTargetMs:        c.targetMs,
		MinTargetMs:         c.minMs,
		MaxTargetMs:         c.maxMs,
		JitterMargin:        c.margin,
		WindowSize:          c.window,
		SampleInterval:      time.Duration(c.sampleMs * float64(time.Millisecond)),
		ConcealThreshold:    c.concealThr,
		MaxSendRegressionMs: 2000,
	})
}

func emit(stdout io.Writer, mode string, ctl *jitter.Controller, jsonOut bool) int {
	r := report.Build(mode, ctl.M)
	if jsonOut {
		if err := report.PrintJSON(stdout, r); err != nil {
			fmt.Fprintf(os.Stderr, "encode report: %v\n", err)
			return 3
		}
		return 0
	}
	report.PrintText(stdout, r)
	return 0
}

func runReplay(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c ctlFlags
	addCtlFlags(fs, &c)
	frames := fs.Int("frames", 1200, "number of frames to synthesize")
	seed := fs.Int64("seed", 41, "random seed (deterministic replay)")
	stream := fs.Uint("stream-id", 7, "stream identifier")
	lossBurst := fs.Int("loss-burst", 5, "max packets dropped per burst")
	reorder := fs.Int("reorder-depth", 4, "reorder buffer depth")
	dupRate := fs.Float64("duplicate-rate", 0.02, "duplicate probability per packet")
	jitterMs := fs.Float64("jitter-ms", 15, "arrival jitter stddev (ms)")
	drift := fs.Float64("drift-ppm", 0, "sender clock drift (ppm)")
	tailMs := fs.Float64("tail-ms", 500, "extra playout time after last arrival (ms)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *frames < 1 || *frames > 1_000_000 {
		fmt.Fprintln(stderr, "frames must be in [1,1000000]")
		return 2
	}
	if *lossBurst < 0 || *reorder < 0 || *jitterMs < 0 || *tailMs < 0 {
		fmt.Fprintln(stderr, "loss-burst, reorder-depth, jitter-ms, tail-ms must be >= 0")
		return 2
	}
	if *dupRate < 0 || *dupRate > 1 {
		fmt.Fprintln(stderr, "duplicate-rate must be in [0,1]")
		return 2
	}
	if err := c.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	evs := scenario.Generate(scenario.Config{
		Frames: *frames, Seed: *seed, StreamID: uint32(*stream),
		LossBurst: *lossBurst, ReorderDepth: *reorder, DuplicateRate: *dupRate,
		JitterMs: *jitterMs, DriftPPM: *drift,
		FrameInterval: 20 * time.Millisecond, PayloadBytes: 160,
	})
	ctl := c.controller()
	receiver.Replay(evs, ctl, c.maxPayload, time.Duration(*tailMs*float64(time.Millisecond)))
	return emit(stdout, "replay", ctl, c.jsonOut)
}

func runListen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c ctlFlags
	addCtlFlags(fs, &c)
	addr := fs.String("addr", "127.0.0.1:39090", "UDP listen address (loopback)")
	duration := fs.Duration("duration", 5*time.Second, "how long to receive")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *duration <= 0 {
		fmt.Fprintln(stderr, "duration must be positive")
		return 2
	}
	if err := c.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctl := c.controller()
	if err := receiver.Listen(*addr, *duration, ctl, c.maxPayload); err != nil {
		fmt.Fprintf(stderr, "listen: %v\n", err)
		return 3
	}
	return emit(stdout, "listen", ctl, c.jsonOut)
}
