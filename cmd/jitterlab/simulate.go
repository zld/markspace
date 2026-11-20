package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/metrics"
	"jitterlab/internal/packet"
	"jitterlab/internal/receiver"
	"jitterlab/internal/report"
	"jitterlab/internal/trace"
)

type simFlags struct {
	seed      int64
	duration  time.Duration
	loss      float64
	reorder   float64
	duplicate float64
	burst     int
	frameMS   int
	streamID  int
	jitterMS  int
	reorderMS int
	waitMS    int
	jsonPath  string
	quiet     bool
}

func parseSimFlags(args []string, stderr io.Writer) (simFlags, error) {
	var f simFlags
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Int64Var(&f.seed, "seed", 42, "deterministic trace seed")
	fs.DurationVar(&f.duration, "duration", 20*time.Second, "trace duration")
	fs.Float64Var(&f.loss, "loss", 0.08, "packet loss probability")
	fs.Float64Var(&f.reorder, "reorder", 0.12, "reorder probability")
	fs.Float64Var(&f.duplicate, "duplicate", 0.03, "duplicate probability")
	fs.IntVar(&f.burst, "burst", 5, "maximum consecutive loss run")
	fs.IntVar(&f.frameMS, "frame-ms", 20, "frame interval in milliseconds")
	fs.IntVar(&f.streamID, "stream", 7, "stream id")
	fs.IntVar(&f.jitterMS, "jitter-ms", 12, "arrival jitter ceiling in milliseconds")
	fs.IntVar(&f.reorderMS, "reorder-ms", 90, "maximum extra delay applied to reordered packets")
	fs.IntVar(&f.waitMS, "wait-ms", 150, "maximum playout wait for a missing frame")
	fs.StringVar(&f.jsonPath, "json", "", "write a JSON report to this path")
	fs.BoolVar(&f.quiet, "quiet", false, "suppress the terminal summary")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() != 0 {
		return f, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if f.duration <= 0 {
		return f, errors.New("duration must be positive")
	}
	if f.duration > 24*time.Hour {
		return f, errors.New("duration exceeds 24h limit")
	}
	if f.frameMS < 5 || f.frameMS > 200 {
		return f, errors.New("frame-ms must be in [5,200]")
	}
	if f.burst < 1 || f.burst > 100 {
		return f, errors.New("burst must be in [1,100]")
	}
	if f.jitterMS < 0 || f.jitterMS > 2000 {
		return f, errors.New("jitter-ms must be in [0,2000]")
	}
	if f.reorderMS < 1 || f.reorderMS > 5000 {
		return f, errors.New("reorder-ms must be in [1,5000]")
	}
	if f.waitMS < 0 || f.waitMS > 2000 {
		return f, errors.New("wait-ms must be in [0,2000]")
	}
	if f.streamID < 0 || f.streamID > 65535 {
		return f, errors.New("stream must be in [0,65535]")
	}
	for name, v := range map[string]float64{"loss": f.loss, "reorder": f.reorder, "duplicate": f.duplicate} {
		if v < 0 || v > 1 {
			return f, fmt.Errorf("%s must be in [0,1]", name)
		}
	}
	return f, nil
}

func runSimulate(args []string, stdout, stderr io.Writer) int {
	f, err := parseSimFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintf(stderr, "simulate: %v\n", err)
		return exitUsage
	}

	tcfg := trace.DefaultConfig()
	tcfg.Seed = f.seed
	tcfg.Duration = f.duration
	tcfg.Loss = f.loss
	tcfg.Reorder = f.reorder
	tcfg.Duplicate = f.duplicate
	tcfg.Burst = f.burst
	tcfg.FrameMS = f.frameMS
	tcfg.StreamID = uint16(f.streamID)
	tcfg.JitterMS = f.jitterMS
	tcfg.ReorderMS = f.reorderMS
	tr := trace.Generate(tcfg)

	// Validate every generated packet before it reaches the controller. A
	// failure here is an input/format problem, not a network one.
	for i := range tr.Packets {
		if _, err := packet.Unmarshal(tr.Packets[i].Data); err != nil {
			fmt.Fprintf(stderr, "simulate: generated packet %d is invalid: %v\n", i, err)
			return exitInput
		}
	}

	jcfg := jitter.DefaultConfig()
	jcfg.FrameMS = f.frameMS
	if f.waitMS > 0 {
		jcfg.WaitMS = f.waitMS
	}
	ctrl := jitter.NewController(jcfg, metrics.New())
	res := receiver.RunSimulated(ctrl, tr, receiver.DefaultSimConfig())
	ctrl.Finalize()

	runInfo := report.Run{
		Mode:        "simulate",
		Seed:        f.seed,
		DurationMS:  f.duration.Milliseconds(),
		Loss:        f.loss,
		Reorder:     f.reorder,
		Duplicate:   f.duplicate,
		Burst:       f.burst,
		FrameMS:     f.frameMS,
		StreamID:    uint16(f.streamID),
		TargetMinMS: jcfg.MinTargetMS,
		TargetMaxMS: jcfg.MaxTargetMS,
	}
	rep := report.New(runInfo, ctrl.Summary())
	if f.jsonPath != "" {
		if err := rep.WriteJSON(f.jsonPath); err != nil {
			fmt.Fprintf(stderr, "simulate: write report: %v\n", err)
			return exitRuntime
		}
	}
	if !f.quiet {
		rep.Print(stdout)
		fmt.Fprintf(stdout, "fed_packets=%d\n", res.Fed)
	}
	return exitOK
}
