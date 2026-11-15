// Command streamlab simulates or receives synthetic audio streams and
// reports jitter buffer behaviour as terminal JSON.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"streamlab/internal/jitter"
	"streamlab/internal/receive"
	"streamlab/internal/report"
	"streamlab/internal/sim"
	"streamlab/internal/wire"
)

const (
	exitOK      = 0
	exitUsage   = 2
	exitRuntime = 3
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(out, "usage: streamlab <simulate|listen> [flags]")
		return exitUsage
	}
	switch args[0] {
	case "simulate":
		return runSimulate(args[1:], out)
	case "listen":
		return runListen(args[1:], out)
	default:
		fmt.Fprintf(out, "unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

// pipeFlags holds the buffer parameters shared by both subcommands.
type pipeFlags struct {
	frameMS    int64
	sampleRate int
	streamID   uint
	targetMS   int
	window     int
	maxConceal int
	json       bool
}

func (pf *pipeFlags) register(fs *flag.FlagSet) {
	fs.Int64Var(&pf.frameMS, "frame-ms", 20, "frame duration in milliseconds")
	fs.IntVar(&pf.sampleRate, "sample-rate", 8000, "sample clock in Hz")
	fs.UintVar(&pf.streamID, "stream-id", 1, "expected stream identifier")
	fs.IntVar(&pf.targetMS, "target-ms", 60, "base target buffer delay")
	fs.IntVar(&pf.window, "window", 64, "sequence window size in frames")
	fs.IntVar(&pf.maxConceal, "max-conceal", 6, "max replayed concealment frames per gap")
	fs.BoolVar(&pf.json, "json", false, "emit JSON report")
}

func (pf *pipeFlags) build() (*receive.Pipeline, error) {
	if pf.frameMS < 5 || pf.frameMS > 250 {
		return nil, errors.New("frame-ms must be in [5,250]")
	}
	if pf.sampleRate < 8000 || pf.sampleRate > 48000 {
		return nil, errors.New("sample-rate must be in [8000,48000]")
	}
	spf := int(pf.frameMS) * pf.sampleRate / 1000
	if spf < 1 || 2*spf > wire.MaxPayload {
		return nil, errors.New("frame size exceeds payload limit")
	}
	if pf.window < 4 || pf.window > 512 {
		return nil, errors.New("window must be in [4,512]")
	}
	if pf.targetMS < 0 || pf.targetMS > 2000 {
		return nil, errors.New("target-ms must be in [0,2000]")
	}
	if pf.maxConceal < 1 || pf.maxConceal > 60 {
		return nil, errors.New("max-conceal must be in [1,60]")
	}
	target := pf.targetMS / int(pf.frameMS)
	cfg := jitter.Config{
		FrameMS:  pf.frameMS,
		Target:   target,
		MinLevel: 1,
		MaxLevel: target + 8,
	}
	return receive.NewPipeline(pf.frameMS, uint32(pf.streamID), spf, cfg, pf.window, pf.maxConceal), nil
}

func emit(out io.Writer, pf *pipeFlags, p *receive.Pipeline) int {
	s := report.Summarize(p)
	if pf.json {
		if err := report.WriteJSON(out, s); err != nil {
			fmt.Fprintf(os.Stderr, "report: %v\n", err)
			return exitRuntime
		}
		return exitOK
	}
	fmt.Fprintf(out, "received=%d played=%d concealed=%d (max run %d)\n",
		s.ReceivedPackets, s.PlayedFrames, s.ConcealedFrames, s.ConcealRunsMax)
	fmt.Fprintf(out, "dropped=%v jitter_ms=%.3f latency_p50=%.1f latency_p95=%.1f\n",
		s.DroppedPackets, s.JitterMS, s.LatencyMSP50, s.LatencyMSP95)
	fmt.Fprintf(out, "boundary_energy_jump_p95=%.1f watermarks=%d points\n",
		s.BoundaryEnergyJumpP95, len(s.BufferWatermarks))
	return exitOK
}

func runSimulate(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	var pf pipeFlags
	pf.register(fs)
	seed := fs.Int64("seed", 1, "scenario seed")
	frames := fs.Int("frames", 240, "number of frames to play out")
	baseDelay := fs.Float64("base-delay-ms", 45, "base one-way delay")
	jitterMS := fs.Float64("jitter-ms", 18, "arrival jitter stddev")
	lossBurst := fs.Int("loss-burst", 4, "max burst loss length")
	dupRate := fs.Float64("duplicate-rate", 0, "duplicate probability")
	reorderRate := fs.Float64("reorder-rate", 0, "reorder probability")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *frames < 1 || *frames > 20000 {
		fmt.Fprintln(os.Stderr, "frames must be in [1,20000]")
		return exitUsage
	}
	if *lossBurst < 0 || *lossBurst > 32 || *baseDelay < 0 || *jitterMS < 0 ||
		*dupRate < 0 || *dupRate > 1 || *reorderRate < 0 || *reorderRate > 1 {
		fmt.Fprintln(os.Stderr, "scenario parameter out of range")
		return exitUsage
	}
	pipe, err := pf.build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}
	dgrams := sim.Generate(sim.Config{
		Seed: *seed, Frames: *frames, FrameMS: pf.frameMS, SampleRate: pf.sampleRate,
		BaseDelayMS: *baseDelay, JitterMS: *jitterMS, LossBurst: *lossBurst,
		DuplicateRate: *dupRate, ReorderRate: *reorderRate, StreamID: uint32(pf.streamID),
	})
	var lastArrival int64
	for _, d := range dgrams {
		if d.ArrivalMs > lastArrival {
			lastArrival = d.ArrivalMs
		}
	}
	capMs := lastArrival + int64(*frames+50)*pf.frameMS + 60000
	di, t := 0, int64(0)
	for ; t <= capMs; t++ {
		for di < len(dgrams) && dgrams[di].ArrivalMs <= t {
			pipe.OnDatagram(dgrams[di].Data, dgrams[di].ArrivalMs)
			di++
		}
		pipe.AdvanceTo(t)
		if len(pipe.Events) >= *frames && di >= len(dgrams) {
			break
		}
	}
	return emit(out, &pf, pipe)
}
