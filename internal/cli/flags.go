// Package cli parses and validates bitrack command-line arguments.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Process exit codes. They are part of the tool's contract for callers.
const (
	ExitOK          = 0
	ExitUsage       = 2
	ExitRegression  = 3
	ExitEncode      = 4
	ExitInterrupted = 75
)

// Options holds every parsed flag for every subcommand; unused fields stay zero.
type Options struct {
	WorkDir        string
	JSON           bool
	Input          string
	Manifest       string
	TargetKbps     int
	SegmentSeconds float64
	Workers        int
	Iterations     int
	TolerancePct   float64
	MinSegmentKbps int
	MaxPSNRDropDB  float64
	BaselinePath   string
	FailAfter      int
	InterruptKind  string
	// Fixture generation
	GenerateFixture  bool
	Segments         int
	FramesPerSegment int
	TailFrames       int
	Width            int
	Height           int
	FPS              int
	// Populated only when --manifest is used.
	Jobs []ManifestJob
}

// ManifestJob is one entry of a JSON manifest. All paths are relative to the
// work directory and may not escape it.
type ManifestJob struct {
	ID             string  `json:"id"`
	Input          string  `json:"input"`
	TargetKbps     int     `json:"target_kbps"`
	SegmentSeconds float64 `json:"segment_seconds"`
}

type manifestFile struct {
	Jobs []ManifestJob `json:"jobs"`
}

// Parse interprets args as "bitrack <subcommand> [flags]".
func Parse(args []string) (string, *Options, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("missing subcommand; expected one of fixture, baseline, run, resume")
	}
	sub := args[0]
	switch sub {
	case "fixture", "baseline", "run", "resume":
	default:
		return "", nil, fmt.Errorf("unknown subcommand %q; expected one of fixture, baseline, run, resume", sub)
	}
	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	o := &Options{
		SegmentSeconds: 4, Workers: 1, Iterations: 4, TolerancePct: 8,
		MinSegmentKbps: 40, MaxPSNRDropDB: 2, Width: 176, Height: 144, FPS: 24,
	}
	fs.StringVar(&o.WorkDir, "workdir", "", "working directory for all run data (required)")
	fs.BoolVar(&o.JSON, "json", false, "emit machine-readable JSON summary")
	fs.StringVar(&o.Input, "input", "", "input video relative to workdir (default: source.mp4)")
	fs.StringVar(&o.Manifest, "manifest", "", "JSON manifest of jobs (alternative to --input)")
	fs.IntVar(&o.TargetKbps, "target-kbps", 0, "delivery target bitrate in kbps")
	fs.Float64Var(&o.SegmentSeconds, "segment-seconds", o.SegmentSeconds, "target segment length in seconds (frame-aligned)")
	fs.IntVar(&o.Workers, "workers", 1, "concurrent encoder workers")
	fs.IntVar(&o.Iterations, "iterations", 4, "max closed-loop encode iterations per segment")
	fs.Float64Var(&o.TolerancePct, "tolerance-pct", 8, "accepted bitrate deviation percent per segment")
	fs.IntVar(&o.MinSegmentKbps, "min-segment-kbps", 40, "minimum encodable per-segment bitrate")
	fs.Float64Var(&o.MaxPSNRDropDB, "max-psnr-drop-db", 2, "max allowed PSNR drop vs baseline in dB")
	fs.StringVar(&o.BaselinePath, "baseline", "", "baseline.json path (run subcommand)")
	fs.IntVar(&o.FailAfter, "fail-after", 0, "inject interruption after N committed segments (0 disables)")
	fs.StringVar(&o.InterruptKind, "inject", "", "fault to inject: fail-after (default), disk-fail, ffmpeg-fail, bad-baseline")
	fs.BoolVar(&o.GenerateFixture, "generate-fixture", false, "generate the deterministic fixture before running")
	fs.IntVar(&o.Segments, "segments", 4, "fixture: number of full segments")
	fs.IntVar(&o.FramesPerSegment, "frames-per-segment", 96, "fixture: frames per full segment")
	fs.IntVar(&o.TailFrames, "tail-frames", -1, "fixture: short tail frames (-1 = quarter segment, 0 = no tail)")
	fs.IntVar(&o.Width, "width", o.Width, "fixture width (even)")
	fs.IntVar(&o.Height, "height", o.Height, "fixture height (even)")
	fs.IntVar(&o.FPS, "fps", o.FPS, "fixture frame rate")
	if err := fs.Parse(args[1:]); err != nil {
		return "", nil, err
	}
	if err := o.validate(sub); err != nil {
		return "", nil, err
	}
	return sub, o, nil
}

func (o *Options) validate(sub string) error {
	if o.WorkDir == "" {
		return fmt.Errorf("--workdir is required")
	}
	abs, err := filepath.Abs(o.WorkDir)
	if err != nil {
		return fmt.Errorf("resolve workdir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("workdir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workdir %s is not a directory", abs)
	}
	o.WorkDir = abs
	if o.Workers < 1 {
		return fmt.Errorf("--workers must be >= 1, got %d", o.Workers)
	}
	if o.FailAfter < 0 {
		return fmt.Errorf("--fail-after must be >= 0")
	}
	switch sub {
	case "fixture":
		return o.validateFixture()
	case "baseline", "run":
		return o.validateRunLike(sub)
	case "resume":
		return nil
	}
	return nil
}

func (o *Options) validateFixture() error {
	if o.Segments <= 0 || o.FramesPerSegment <= 0 {
		return fmt.Errorf("fixture requires positive --segments and --frames-per-segment")
	}
	if o.Width <= 0 || o.Height <= 0 || o.Width%2 != 0 || o.Height%2 != 0 {
		return fmt.Errorf("fixture dimensions must be positive even numbers, got %dx%d", o.Width, o.Height)
	}
	if o.FPS <= 0 {
		return fmt.Errorf("fixture --fps must be positive")
	}
	if o.TailFrames == -1 {
		o.TailFrames = o.FramesPerSegment / 4
	}
	if o.TailFrames < 0 || o.TailFrames >= o.FramesPerSegment {
		return fmt.Errorf("--tail-frames must be in [0, frames-per-segment)")
	}
	return nil
}

func (o *Options) validateRunLike(sub string) error {
	if o.TargetKbps <= 0 {
		return fmt.Errorf("--target-kbps must be positive, got %d", o.TargetKbps)
	}
	if o.SegmentSeconds <= 0 {
		return fmt.Errorf("--segment-seconds must be positive, got %v", o.SegmentSeconds)
	}
	if o.Iterations <= 0 || o.TolerancePct <= 0 || o.MinSegmentKbps <= 0 {
		return fmt.Errorf("iterations, tolerance and min-segment-kbps must all be positive")
	}
	if o.MaxPSNRDropDB < 0 {
		return fmt.Errorf("--max-psnr-drop-db must be >= 0")
	}
	if o.Manifest != "" {
		return o.loadManifest()
	}
	if o.Input == "" {
		o.Input = "source.mp4"
	}
	return o.checkInsideWorkDir(o.Input, "--input")
}

// checkInsideWorkDir enforces that data paths never escape the work directory.
func (o *Options) checkInsideWorkDir(rel, flagName string) error {
	if filepath.IsAbs(rel) {
		return fmt.Errorf("%s must be relative to the work directory", flagName)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s %q escapes the work directory", flagName, rel)
	}
	return nil
}

func (o *Options) loadManifest() error {
	abs := o.Manifest
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(o.WorkDir, abs)
	}
	if !isInside(o.WorkDir, abs) {
		return fmt.Errorf("manifest path %q escapes the work directory", o.Manifest)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var mf manifestFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if len(mf.Jobs) == 0 {
		return fmt.Errorf("manifest contains no jobs")
	}
	seen := map[string]bool{}
	for i, j := range mf.Jobs {
		if j.ID == "" {
			return fmt.Errorf("manifest job %d missing id", i)
		}
		if seen[j.ID] {
			return fmt.Errorf("manifest has duplicate task id %q", j.ID)
		}
		seen[j.ID] = true
		if j.TargetKbps <= 0 {
			return fmt.Errorf("manifest job %q: target_kbps must be positive", j.ID)
		}
		if j.SegmentSeconds <= 0 {
			j.SegmentSeconds = o.SegmentSeconds
			mf.Jobs[i] = j
		}
		if err := o.checkInsideWorkDir(j.Input, "manifest input"); err != nil {
			return fmt.Errorf("manifest job %q: %w", j.ID, err)
		}
	}
	o.Jobs = mf.Jobs
	return nil
}

func isInside(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ResolveInput returns the absolute input path for single-video invocations.
func (o *Options) ResolveInput() string {
	return filepath.Join(o.WorkDir, filepath.Clean(o.Input))
}
