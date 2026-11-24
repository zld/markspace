// Command mediapipe is the terminal entry point for the reusable media
// processing kernel. The demo subcommand generates a fully local fixture,
// runs the stage graph with optional interruption and resume, and prints the
// required summary fields. Exit codes: 0 success, 2 accurately reported input
// corruption, 1 invalid parameters or dependency graph.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"mediapipe/internal/engine"
	"mediapipe/internal/fixture"
	"mediapipe/internal/model"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediapipe demo [flags]")
	fmt.Fprintln(os.Stderr, "flags:")
	fmt.Fprintln(os.Stderr, "  --seed int            deterministic fixture seed (default 17)")
	fmt.Fprintln(os.Stderr, "  --frames int          video frames to generate (default 120)")
	fmt.Fprintln(os.Stderr, "  --audio-rate int      44100|48000|96000 (default 44100)")
	fmt.Fprintln(os.Stderr, "  --channels int        1|2|6 (default 6)")
	fmt.Fprintln(os.Stderr, "  --interrupt-after int halt after N new stages (default 0)")
	fmt.Fprintln(os.Stderr, "  --resume              relaunch from the checkpoint and finish")
	fmt.Fprintln(os.Stderr, "  --corrupt string      truncated|hash_mismatch|timestamp_back|unknown_color")
}

type options struct {
	seed           int64
	frames         int
	audioRate      int64
	channels       int
	interruptAfter int
	resume         bool
	corrupt        string
	colorTarget    string
	targetRate     int64
	targetLUFS     float64
	workDir        string
}

func parseDemoFlags(args []string) (options, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	o := options{}
	fs.Int64Var(&o.seed, "seed", 17, "fixture seed")
	fs.IntVar(&o.frames, "frames", 120, "frame count")
	fs.Int64Var(&o.audioRate, "audio-rate", 44100, "source audio rate")
	fs.IntVar(&o.channels, "channels", 6, "input channel count")
	fs.IntVar(&o.interruptAfter, "interrupt-after", 0, "halt after N stages")
	fs.BoolVar(&o.resume, "resume", false, "resume from checkpoint")
	fs.StringVar(&o.corrupt, "corrupt", "", "optional fault injection")
	fs.StringVar(&o.colorTarget, "color-target", "bt709", "target color label")
	fs.Int64Var(&o.targetRate, "target-rate", 48000, "target audio rate")
	fs.Float64Var(&o.targetLUFS, "target-lufs", -23, "loudness target")
	fs.StringVar(&o.workDir, "work-dir", "", "persistent work directory")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	return o, o.validate()
}

func (o options) validate() error {
	switch o.audioRate {
	case 44100, 48000, 96000:
	default:
		return fmt.Errorf("audio-rate must be 44100, 48000 or 96000, got %d", o.audioRate)
	}
	switch o.channels {
	case 1, 2, 6:
	default:
		return fmt.Errorf("channels must be 1, 2 or 6, got %d", o.channels)
	}
	if o.frames <= 0 {
		return fmt.Errorf("frames must be positive, got %d", o.frames)
	}
	switch fixture.Corruption(o.corrupt) {
	case fixture.None, fixture.Truncated, fixture.HashMismatch,
		fixture.TimestampBack, fixture.UnknownColor:
	default:
		return fmt.Errorf("unknown corruption %q", o.corrupt)
	}
	return nil
}

func runDemo(args []string) int {
	o, err := parseDemoFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid parameters: %v\n", err)
		return 1
	}
	dir, cleanup, err := workDirectory(o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "work dir: %v\n", err)
		return 1
	}
	defer cleanup()

	gen, err := fixture.Build(dir, fixture.Spec{
		Seed: o.seed, Frames: o.frames, AudioRate: o.audioRate,
		Channels: o.channels, ColorTarget: o.colorTarget,
		Corruption: fixture.Corruption(o.corrupt),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		return 1
	}

	// First process: run with the configured interruption point.
	cfg := engine.Config{
		WorkDir: dir, ColorTarget: o.colorTarget, TargetRate: o.targetRate,
		TargetLUFS: o.targetLUFS, TruePeak: 1.0, InterruptAfter: o.interruptAfter,
	}
	e1, err := engine.New(cfg)
	if err != nil {
		return reportClassifiedError(nil, err)
	}
	e1.AttachPCM(gen.AudioTrack.PCM)
	s1, err := e1.Run(time.Now())
	if err != nil && !errors.Is(err, engine.ErrInterrupted) {
		// Corruption and color failures still produce a partial summary.
		return reportClassifiedError(e1.CurrentSummary(), err)
	}
	if errors.Is(err, engine.ErrInterrupted) && !o.resume {
		// Partial run without resume: report what the checkpoint holds.
		printSummary(e1.CurrentSummary(), 0)
		return 0
	}

	// Second process: resume from the atomic checkpoint and finish the graph.
	if !o.resume {
		printSummary(s1, 0)
		return 0
	}
	cfg.InterruptAfter = 0
	e2, err := engine.New(cfg)
	if err != nil {
		return reportClassifiedError(nil, err)
	}
	e2.AttachPCM(gen.AudioTrack.PCM)
	started := time.Now()
	s2, err := e2.Run(started)
	if err != nil {
		return reportClassifiedError(e2.CurrentSummary(), err)
	}
	printSummary(s2, time.Since(started).Milliseconds())
	return 0
}

func workDirectory(o options) (string, func(), error) {
	if o.workDir != "" {
		if err := os.MkdirAll(o.workDir, 0o755); err != nil {
			return "", func() {}, err
		}
		return o.workDir, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "mediapipe-demo-")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// reportClassifiedError prints the summary when available and maps the
// classified error to the contract's exit code (2 corruption, 1 otherwise).
func reportClassifiedError(s *engine.Summary, err error) int {
	me := model.AsMediaError(err)
	if me == nil {
		fmt.Fprintf(os.Stderr, "pipeline failed: %v\n", err)
		return 1
	}
	if s != nil {
		printSummary(s, 0)
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return me.ExitCode()
}

func printSummary(s *engine.Summary, resumeMS int64) {
	if resumeMS == 0 {
		resumeMS = s.ResumeMS
	}
	fmt.Printf("track_count=%d\n", s.TrackCount)
	fmt.Printf("frame_count=%d\n", s.FrameCount)
	fmt.Printf("corrupt_first_frame=%d\n", s.CorruptFirstFrame)
	fmt.Printf("corrupt_offset_bytes=%d\n", s.CorruptOffset)
	fmt.Printf("resume_ms=%d\n", resumeMS)
	fmt.Printf("output_sha256=%s\n", s.OutputSHA256)
	fmt.Printf("loudness_lufs=%.4f\n", s.LoudnessLUFS)
	fmt.Printf("clip_count=%d\n", s.ClipCount)
	fmt.Printf("reused_stages=%d\n", s.ReusedStages)
	fmt.Printf("blocked_stages=%s\n", strings.Join(s.BlockedStages, ","))
}
