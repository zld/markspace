// Package config parses and validates command-line parameters.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"streamcheck/internal/manifest"
	"streamcheck/internal/probe"
)

// MaxConcurrency bounds parallel ffprobe processes.
const MaxConcurrency = 64

// Build is the validated configuration for the build command.
type Build struct {
	Input          string
	Rates          []int
	Pattern        string
	SegmentSeconds float64
	ToleranceMS    int64
	Strict         bool
	JSON           bool
	FFprobe        string
	ProbeTimeout   time.Duration
	Concurrency    int
	ManifestDir    string
}

// Fixture is the validated configuration for the fixture command.
type Fixture struct {
	Out            string
	Rates          []int
	Segments       int
	SegmentSeconds float64
	Pattern        string
}

// UsageError marks a parameter problem; the CLI maps it to exit code 2.
type UsageError struct {
	Command string
	Err     error
	Usage   string
}

func (e *UsageError) Error() string {
	return fmt.Sprintf("%s: %v\n\n%s", e.Command, e.Err, e.Usage)
}
func (e *UsageError) Unwrap() error { return e.Err }

const buildUsage = `Usage: streamcheck build --input DIR --rates N[,N...] [options]

Generates one VOD M3U8 per bitrate, verifies it, and compares rates.

Required:
  --input DIR              directory containing media segments
  --rates N[,N...]         requested bitrates in kbps (no duplicates)

Options:
  --pattern NAME           segment naming rule (default "seg_{rate}_{index}.ts")
  --segment-seconds SEC    nominal segment duration (default 4)
  --tolerance-ms MS        max cross-rate boundary drift in ms (default 80)
  --strict                 treat missing segment ids as failures
  --json                   emit a JSON report instead of text
  --manifest-dir DIR       where to write playlists (default: input dir)
  --ffprobe PATH           ffprobe executable (default "ffprobe")
  --probe-timeout DUR      per-file ffprobe timeout (default 5s)
  --concurrency N          parallel ffprobe processes (default 4)
`

const fixtureUsage = `Usage: streamcheck fixture --out DIR [options]

Writes deterministic synthetic media segments plus missing, duplicate,
unordered and drift cases under DIR/cases/.

Options:
  --rates N[,N...]         bitrates to generate (default 800,1600)
  --segments N             segments per bitrate (default 6)
  --segment-seconds SEC    duration of each segment (default 4)
  --pattern NAME           segment naming rule (default "seg_{rate}_{index}.ts")
`

// ParseBuild parses args (excluding the subcommand) into a validated config.
func ParseBuild(args []string) (*Build, error) {
	var (
		input      string
		rates      string
		pattern    string
		segmentSec float64
		tolerance  int64
		strict     bool
		asJSON     bool
		ffprobe    string
		timeout    time.Duration
		conc       int
		manifestTo string
	)
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&input, "input", "", "")
	fs.StringVar(&rates, "rates", "", "")
	fs.StringVar(&pattern, "pattern", manifest.DefaultPattern, "")
	fs.Float64Var(&segmentSec, "segment-seconds", 4, "")
	fs.Int64Var(&tolerance, "tolerance-ms", 80, "")
	fs.BoolVar(&strict, "strict", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.StringVar(&ffprobe, "ffprobe", "ffprobe", "")
	fs.DurationVar(&timeout, "probe-timeout", probe.DefaultTimeout, "")
	fs.IntVar(&conc, "concurrency", 4, "")
	fs.StringVar(&manifestTo, "manifest-dir", "", "")
	if err := fs.Parse(args); err != nil {
		return nil, usageErr("build", err, buildUsage)
	}
	if fs.NArg() > 0 {
		return nil, usageErr("build", fmt.Errorf("unexpected argument %q", fs.Arg(0)), buildUsage)
	}

	var errs []string
	if strings.TrimSpace(input) == "" {
		errs = append(errs, "--input is required")
	}
	if strings.TrimSpace(rates) == "" {
		errs = append(errs, "--rates is required")
	}
	parsedRates, err := ParseRates(rates)
	if err != nil && strings.TrimSpace(rates) != "" {
		errs = append(errs, err.Error())
	}
	if segmentSec <= 0 || math.IsNaN(segmentSec) || math.IsInf(segmentSec, 0) {
		errs = append(errs, "--segment-seconds must be a positive number")
	}
	if tolerance < 0 {
		errs = append(errs, "--tolerance-ms must not be negative")
	}
	if timeout <= 0 {
		errs = append(errs, "--probe-timeout must be positive")
	}
	if conc <= 0 {
		errs = append(errs, "--concurrency must be positive")
	} else if conc > MaxConcurrency {
		errs = append(errs, fmt.Sprintf("--concurrency must not exceed %d", MaxConcurrency))
	}
	if compiled, err := manifest.CompilePattern(pattern); err != nil {
		errs = append(errs, "--pattern: "+err.Error())
	} else if len(parsedRates) > 1 && !compiled.HasRate() {
		errs = append(errs, "--pattern must contain {rate} when multiple rates are requested")
	}
	if len(errs) > 0 {
		return nil, usageErr("build", errors.New(strings.Join(errs, "; ")), buildUsage)
	}
	if manifestTo == "" {
		manifestTo = input
	}
	return &Build{
		Input:          input,
		Rates:          parsedRates,
		Pattern:        pattern,
		SegmentSeconds: segmentSec,
		ToleranceMS:    tolerance,
		Strict:         strict,
		JSON:           asJSON,
		FFprobe:        ffprobe,
		ProbeTimeout:   timeout,
		Concurrency:    conc,
		ManifestDir:    manifestTo,
	}, nil
}

// ParseFixture parses args (excluding the subcommand) for fixture generation.
func ParseFixture(args []string) (*Fixture, error) {
	var (
		out        string
		rates      string
		segments   int
		segmentSec float64
		pattern    string
	)
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&out, "out", "", "")
	fs.StringVar(&rates, "rates", "800,1600", "")
	fs.IntVar(&segments, "segments", 6, "")
	fs.Float64Var(&segmentSec, "segment-seconds", 4, "")
	fs.StringVar(&pattern, "pattern", manifest.DefaultPattern, "")
	if err := fs.Parse(args); err != nil {
		return nil, usageErr("fixture", err, fixtureUsage)
	}
	if fs.NArg() > 0 {
		return nil, usageErr("fixture", fmt.Errorf("unexpected argument %q", fs.Arg(0)), fixtureUsage)
	}
	var errs []string
	if strings.TrimSpace(out) == "" {
		errs = append(errs, "--out is required")
	}
	parsedRates, err := ParseRates(rates)
	if err != nil {
		errs = append(errs, err.Error())
	}
	if segments <= 0 {
		errs = append(errs, "--segments must be positive")
	}
	if segmentSec <= 0 || math.IsNaN(segmentSec) || math.IsInf(segmentSec, 0) {
		errs = append(errs, "--segment-seconds must be a positive number")
	}
	if _, err := manifest.CompilePattern(pattern); err != nil {
		errs = append(errs, "--pattern: "+err.Error())
	}
	if len(errs) > 0 {
		return nil, usageErr("fixture", errors.New(strings.Join(errs, "; ")), fixtureUsage)
	}
	return &Fixture{Out: out, Rates: parsedRates, Segments: segments, SegmentSeconds: segmentSec, Pattern: pattern}, nil
}
