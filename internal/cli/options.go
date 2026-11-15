// Package cli implements the terminal interface: argument parsing,
// validation and exit-code mapping. It owns no pipeline logic.
package cli

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"transcode/internal/fixture"
	"transcode/internal/state"
)

// Exit codes are part of the documented contract.
const (
	ExitOK             = 0
	ExitGeneric        = 1
	ExitInvalidInput   = 2
	ExitPartialFailure = 3
	ExitCheckpointBad  = 4
)

// Options is the parsed command line.
type Options struct {
	Command        string // "demo"
	DurationSec    float64
	SegmentSec     float64
	TargetKbps     float64
	MinKbps        float64
	Workers        int
	Checkpoint     string
	JSON           bool
	Workspace      string
	Encoder        string // explicit encoder command template
	MaxOutputBytes int64
	SampleSidecar  string // optional precomputed complexity samples
	// Resolved at runtime:
	InputPath  string
	OutputDir  string
	WorkDir    string
	EncoderBin string
}

// Parse builds Options from argv. The first positional argument selects the
// subcommand; the demo command is the only one currently supported.
func Parse(argv []string) (Options, error) {
	if len(argv) == 0 {
		return Options{}, fmt.Errorf("missing subcommand (expected: demo)")
	}
	cmd := argv[0]
	if cmd != "demo" {
		return Options{}, fmt.Errorf("unknown subcommand %q (supported: demo)", cmd)
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	o := Options{Command: cmd}
	fs.Float64Var(&o.DurationSec, "duration", 0, "input duration in seconds (required, >0)")
	fs.Float64Var(&o.SegmentSec, "segment-seconds", 0, "segment length in seconds (required, >0)")
	fs.Float64Var(&o.TargetKbps, "target-kbps", 0, "average target bitrate in kbps (required, >0)")
	fs.Float64Var(&o.MinKbps, "min-kbps", 100, "encoder minimum bitrate in kbps")
	fs.IntVar(&o.Workers, "workers", 1, "maximum concurrent encode workers")
	fs.StringVar(&o.Checkpoint, "checkpoint", "", "checkpoint JSON path (required)")
	fs.BoolVar(&o.JSON, "json", false, "emit a machine-readable JSON report")
	fs.StringVar(&o.Workspace, "workspace", "", "workspace root (defaults to checkpoint directory)")
	fs.StringVar(&o.Encoder, "encoder", "", "encoder command template with {in} {out} {kbps} {duration}")
	fs.Int64Var(&o.MaxOutputBytes, "max-output-bytes", 64<<20, "per-segment output size ceiling")
	fs.StringVar(&o.SampleSidecar, "samples", "", "optional complexity sample sidecar path")
	if err := fs.Parse(argv[1:]); err != nil {
		return o, err
	}
	if err := o.validate(); err != nil {
		return o, err
	}
	o.resolvePaths()
	return o, nil
}

func (o *Options) validate() error {
	checks := []struct {
		ok  bool
		msg string
	}{
		{finitePositive(o.DurationSec), "--duration must be a positive finite number"},
		{finitePositive(o.SegmentSec), "--segment-seconds must be a positive finite number"},
		{finitePositive(o.TargetKbps), "--target-kbps must be a positive finite number"},
		{finitePositive(o.MinKbps), "--min-kbps must be a positive finite number"},
		{o.SegmentSec <= o.DurationSec, "--segment-seconds cannot exceed --duration"},
		{o.Workers >= 1, "--workers must be >= 1"},
		{o.Checkpoint != "", "--checkpoint is required"},
		{o.Encoder != "", "--encoder must explicitly name the encoder command (use {in} {out} {kbps} {duration})"},
		{o.MaxOutputBytes > 0, "--max-output-bytes must be > 0"},
	}
	for _, c := range checks {
		if !c.ok {
			return fmt.Errorf("%w: %s", errInvalid, c.msg)
		}
	}
	return nil
}

func finitePositive(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0
}

// resolvePaths fixes the on-disk layout. Everything (input, outputs,
// checkpoint, scratch) lives under one workspace root so path containment
// can be enforced in one place.
func (o *Options) resolvePaths() {
	cpAbs, _ := filepath.Abs(o.Checkpoint)
	root := o.Workspace
	if root == "" {
		root = filepath.Dir(cpAbs)
	}
	rootAbs, _ := filepath.Abs(root)
	o.Workspace = rootAbs
	o.Checkpoint = cpAbs
	o.InputPath = filepath.Join(rootAbs, "sample.vid")
	o.OutputDir = filepath.Join(rootAbs, "segments")
	o.WorkDir = filepath.Join(rootAbs, "work")
}

// Params builds the checkpoint parameter summary from resolved options.
func (o Options) Params() state.Params {
	return state.Params{
		InputPath:      o.InputPath,
		DurationSec:    o.DurationSec,
		SegmentSec:     o.SegmentSec,
		TargetKbps:     o.TargetKbps,
		MinKbps:        o.MinKbps,
		EncoderCommand: o.Encoder,
		Workers:        o.Workers,
		MaxOutputBytes: o.MaxOutputBytes,
	}
}

// LoadOrGenerateSamples reads a sidecar when given, otherwise returns nil so
// the caller can derive samples straight from the generated fixture.
func (o Options) LoadOrGenerateSamples() ([]float64, error) {
	if o.SampleSidecar == "" {
		return nil, nil
	}
	return fixture.LoadSamples(o.SampleSidecar)
}
