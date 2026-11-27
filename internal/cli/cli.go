// Package cli is the terminal adapter for the assetpack command.  It parses
// flags, prepares the work directory, invokes the pipeline and renders the
// report.  It holds no media logic.
package cli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"example.com/assetpack/internal/audio"
	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/fixture"
	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/pipeline"
)

// Main runs the command and returns a process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "run":
		return runRelease(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "assetpack — audio spec normalization and release manifest migration")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  assetpack demo   --work DIR --target-rate HZ --target-layout L --lufs DB [--inject LIST] [--fail-after STAGE] [--force]")
	fmt.Fprintln(w, "  assetpack run    --work DIR --target-rate HZ --target-layout L --lufs DB")
	fmt.Fprintln(w, "  assetpack verify --work DIR")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "stages: parse, audio, manifest-stage, verify, commit")
}

func usageError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "error: %v\n", err)
	return 2
}

// demoFlags is the shared flag set.
type demoFlags struct {
	work      string
	rate      int
	layout    string
	lufs      float64
	inject    string
	failAfter string
	force     bool
	release   string
	maxGap    int64
}

func bindFlags(fs *flag.FlagSet, d *demoFlags) {
	fs.StringVar(&d.work, "work", "", "work directory (must contain the run's inputs and outputs)")
	fs.IntVar(&d.rate, "target-rate", 48000, "target sample rate in Hz")
	fs.StringVar(&d.layout, "target-layout", "stereo", "target channel layout: mono, stereo or 5.1")
	fs.Float64Var(&d.lufs, "lufs", -16, "integrated loudness target in LUFS")
	fs.StringVar(&d.inject, "inject", "", "comma separated fault injection: truncated-ts,cc-jump,cc-duplicate,psi-update,pcr-regress")
	fs.StringVar(&d.failAfter, "fail-after", "", "abort after the named stage")
	fs.BoolVar(&d.force, "force", false, "rebuild and recommit even when inputs are unchanged")
	fs.StringVar(&d.release, "release", "demo-release", "release name")
	fs.Int64Var(&d.maxGap, "max-gap-samples", 0, "fail when missing material exceeds this many frames (0 = tolerate)")
}

func (d *demoFlags) target() (audio.Target, error) {
	layout, err := audio.ParseLayout(d.layout)
	if err != nil {
		return audio.Target{}, err
	}
	t := audio.Target{Format: audio.Format{Rate: d.rate, Layout: layout}, Loudness: d.lufs}
	if err := t.Validate(); err != nil {
		return audio.Target{}, err
	}
	return t, nil
}

// prepare generates the fixture inputs under work/input.  The clean streams
// are always the basis: the legacy manifest records their digests, and any
// requested faults are applied afterwards.  That ordering is what makes the
// damaged demo exercise the digest check instead of silently releasing
// bytes that no longer match the declared source.
func prepare(work string, inject fixture.FaultSet, target audio.Target, release string) error {
	inputDir := filepath.Join(work, "input")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return fault.New(fault.StageCLI, fault.KindIO, "create input dir: %v", err)
	}
	specs := fixture.DefaultAssets()
	clean := fixture.TSFiles(specs)
	for i, spec := range specs {
		key := spec.Name + ".ts"
		data := clean[key]
		if len(inject.Names()) > 0 {
			opts := fixture.StreamOptions{Asset: spec, PID: uint16(0x102 + i),
				PCRPID: uint16(0x201 + i), PMTPID: uint16(0x1000 + i), Program: uint16(i + 1)}
			data = fixture.BuildBroken(opts, inject).Data
		}
		if err := os.WriteFile(filepath.Join(inputDir, key), data, 0o644); err != nil {
			return fault.New(fault.StageCLI, fault.KindIO, "write %s: %v", key, err)
		}
	}
	legacy := fixture.LegacyFor(release, specs, clean)
	raw, err := legacy.Encode()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(inputDir, "manifest.v1.json"), raw, 0o644); err != nil {
		return fault.New(fault.StageCLI, fault.KindIO, "write legacy manifest: %v", err)
	}
	return nil
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	var d demoFlags
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bindFlags(fs, &d)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d.work == "" {
		return usageError(stderr, fault.New(fault.StageCLI, fault.KindInvalidArg, "--work is required"))
	}
	target, err := d.target()
	if err != nil {
		return usageError(stderr, err)
	}
	set, err := fixture.ParseFaultSet(d.inject)
	if err != nil {
		return usageError(stderr, err)
	}
	opts := pipeline.Options{
		WorkDir: d.work, InputDir: filepath.Join(d.work, "input"),
		ReleaseDir: filepath.Join(d.work, "release"), StateDir: filepath.Join(d.work, "state"),
		Release: d.release, Target: target, FailAfter: d.failAfter,
		Force: d.force, MaxGapSample: d.maxGap,
	}

	if len(set.Names()) > 0 {
		return runFaultDemo(opts, d, set, target, stdout, stderr)
	}

	// Normal run: one clean release, regenerated from scratch every time so
	// the demo is self-contained.  When a release already exists this is a
	// resumed run and the diff must report only reuse.
	releasedBefore := fileExists(filepath.Join(d.work, "release", "manifest.json"))
	if !releasedBefore {
		if err := os.RemoveAll(filepath.Join(d.work, "state")); err != nil {
			return usageError(stderr, err)
		}
	}
	if err := prepare(d.work, set, target, d.release); err != nil {
		return usageError(stderr, err)
	}
	out, runErr := pipeline.Run(opts)
	render(out, runErr, releasedBefore, stdout, stderr)
	if out == nil {
		return 1
	}
	return out.ExitCode()
}

// runRelease processes existing inputs without generating fixtures.
func runRelease(args []string, stdout, stderr io.Writer) int {
	var d demoFlags
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bindFlags(fs, &d)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d.work == "" {
		return usageError(stderr, fault.New(fault.StageCLI, fault.KindInvalidArg, "--work is required"))
	}
	target, err := d.target()
	if err != nil {
		return usageError(stderr, err)
	}
	opts := pipeline.Options{
		WorkDir: d.work, InputDir: filepath.Join(d.work, "input"),
		ReleaseDir: filepath.Join(d.work, "release"), StateDir: filepath.Join(d.work, "state"),
		Release: d.release, Target: target, FailAfter: d.failAfter,
		Force: d.force, MaxGapSample: d.maxGap,
	}
	out, runErr := pipeline.Run(opts)
	render(out, runErr, true, stdout, stderr)
	if out == nil {
		return 1
	}
	return out.ExitCode()
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	var d demoFlags
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bindFlags(fs, &d)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d.work == "" {
		return usageError(stderr, fault.New(fault.StageCLI, fault.KindInvalidArg, "--work is required"))
	}
	path := filepath.Join(d.work, "release", "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return usageError(stderr, fault.New(fault.StageCLI, fault.KindIO, "read manifest: %v", err))
	}
	m, err := manifest.ReadV2(bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintf(stderr, "manifest invalid: %v\n", err)
		return 1
	}
	if err := (&manifest.Adapter{}).VerifyArtifacts(filepath.Join(d.work, "release", "artifacts"), m); err != nil {
		fmt.Fprintf(stderr, "manifest invalid: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "manifest valid: %s\n", m.Summary())
	return 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// runFaultDemo is supplied by scenario.go.
func runFaultDemo(opts pipeline.Options, d demoFlags, set fixture.FaultSet,
	target audio.Target, stdout, stderr io.Writer) int {
	return 1
}
