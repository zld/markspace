// Command avbatch archives synthetic mobile captures into replayable segments.
//
// It is a terminal-only tool: all input is generated locally from a seed,
// output is printed as key=value lines, and the exit code distinguishes a
// clean run (0) from one that recovered usable output from damaged input (2).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"avbatch/internal/mediaerr"
	"avbatch/internal/pipeline"
	"avbatch/internal/synth"
)

const (
	exitOK        = 0
	exitFailure   = 1
	exitRecovered = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: avbatch demo [flags]")
		return exitFailure
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, "usage: avbatch demo --generate --seed N [--block-samples N] [--inject-...] [--verify-repeat]")
		return exitOK
	default:
		fmt.Fprintf(stderr, "avbatch: unknown command %q\n", args[0])
		return exitFailure
	}
}

type demoFlags struct {
	generate     bool
	seed         int64
	blockSamples int
	indexDir     string
	verifyRepeat bool

	injectSeqGap          bool
	injectTfdtRegression  bool
	injectWrap            bool
	injectVFR             bool
	injectTruncatedBox    bool
	injectCheckpointCrash bool
	durationSeconds       int
}

func parseDemoFlags(args []string, stderr io.Writer) (demoFlags, error) {
	var f demoFlags
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&f.generate, "generate", false, "generate the synthetic capture group")
	fs.Int64Var(&f.seed, "seed", 0, "deterministic generator seed")
	fs.IntVar(&f.blockSamples, "block-samples", pipeline.DefaultConfig().BlockSamples, "media samples per index block")
	fs.StringVar(&f.indexDir, "index-dir", ".avbatch", "directory for the local index")
	fs.BoolVar(&f.verifyRepeat, "verify-repeat", false, "re-run and confirm the manifest digest is identical")
	fs.BoolVar(&f.injectSeqGap, "inject-seq-gap", false, "drop one mfhd sequence number")
	fs.BoolVar(&f.injectTfdtRegression, "inject-tfdt-regression", false, "emit a backwards tfdt")
	fs.BoolVar(&f.injectWrap, "inject-wrap", false, "cross the RTP timestamp wrap point")
	fs.BoolVar(&f.injectVFR, "inject-vfr", false, "use a variable frame rate")
	fs.BoolVar(&f.injectTruncatedBox, "inject-truncated-box", false, "truncate the final fragment")
	fs.BoolVar(&f.injectCheckpointCrash, "inject-checkpoint-crash", false, "simulate a crash mid-index")
	fs.IntVar(&f.durationSeconds, "duration-seconds", 10, "generated media duration")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if f.blockSamples <= 0 {
		return f, errors.New("--block-samples must be positive")
	}
	if f.durationSeconds <= 0 || f.durationSeconds > 7200 {
		return f, errors.New("--duration-seconds must be between 1 and 7200")
	}
	if !f.generate {
		return f, errors.New("--generate is required: this build never reads external media")
	}
	return f, nil
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	f, err := parseDemoFlags(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "avbatch demo: %v\n", err)
		return exitFailure
	}
	group, err := synth.BuildDemoGroup(synth.Options{
		Seed:                  f.seed,
		InjectSeqGap:          f.injectSeqGap,
		InjectTfdtRegression:  f.injectTfdtRegression,
		InjectWrap:            f.injectWrap,
		InjectVFR:             f.injectVFR,
		InjectTruncatedBox:    f.injectTruncatedBox,
		InjectCheckpointCrash: f.injectCheckpointCrash,
		DurationSeconds:       f.durationSeconds,
	})
	if err != nil {
		fmt.Fprintf(stderr, "avbatch demo: generate: %v\n", err)
		return exitFailure
	}
	cfg := pipeline.DefaultConfig()
	cfg.BlockSamples = f.blockSamples
	cfg.IndexPath = filepath.Join(f.indexDir, "manifest.idx")
	if f.injectCheckpointCrash {
		cfg.CrashAtBlocks = 2
	}

	input := pipeline.FromGroup(group)
	res, runErr := pipeline.Run(input, cfg)

	printReport(stdout, res, runErr)

	if f.verifyRepeat {
		// Re-run both paths from the same input and compare the manifest
		// digests. The digest is computed from the recovered entry list, not
		// from the output files, so it is also independent of block layout.
		cfg1 := cfg
		cfg1.IndexPath = filepath.Join(f.indexDir, "repeat-a.idx")
		cfg1.CrashAtBlocks = 0
		resA, errA := pipeline.Run(input, cfg1)
		cfg2 := cfg1
		cfg2.IndexPath = filepath.Join(f.indexDir, "repeat-b.idx")
		resB, errB := pipeline.Run(input, cfg2)
		_ = errA
		_ = errB
		match := resA.ManifestSHA256 != "" && resA.ManifestSHA256 == resB.ManifestSHA256
		fmt.Fprintf(stdout, "repeat_manifest_match=%t\n", match)
		fmt.Fprintf(stdout, "repeat_manifest_sha256=%s\n", resA.ManifestSHA256)
	}

	switch {
	case runErr == nil:
		return exitOK
	case res.Recovered || res.TruncatedInput:
		fmt.Fprintf(stderr, "avbatch demo: recovered output from damaged input: %v\n", runErr)
		return exitRecovered
	default:
		fmt.Fprintf(stderr, "avbatch demo: %v\n", runErr)
		return exitFailure
	}
}

func printReport(stdout io.Writer, res pipeline.Result, runErr error) {
	fmt.Fprintf(stdout, "segments=%d\n", res.Segments)
	fmt.Fprintf(stdout, "silence_ms=%d\n", res.SilenceMillis)
	fmt.Fprintf(stdout, "duplicates=%d\n", res.Duplicates)
	fmt.Fprintf(stdout, "target_bitrate_kbps=%.1f\n", res.TargetKbps)
	fmt.Fprintf(stdout, "actual_bitrate_kbps=%.1f\n", res.ActualKbps)
	fmt.Fprintf(stdout, "bitrate_deviation_pct=%.2f\n", res.DeviationPct)
	fmt.Fprintf(stdout, "quality_samples=%d\n", res.QualitySamples)
	fmt.Fprintf(stdout, "manifest_alignment_error_ms=%.2f\n", res.AlignmentErrorMS)
	fmt.Fprintf(stdout, "recovered_blocks=%d\n", res.RecoveredBlocks)
	fmt.Fprintf(stdout, "reused_blocks=%d\n", res.BlocksReused)
	fmt.Fprintf(stdout, "resume_offset=%d\n", res.ResumeOffset)
	fmt.Fprintf(stdout, "manifest_sha256=%s\n", res.ManifestSHA256)
	fmt.Fprintf(stdout, "rtp_packets=%d\n", res.RTPPackets)
	fmt.Fprintf(stdout, "rtp_wraps=%d\n", res.WrapCount)
	fmt.Fprintf(stdout, "rtp_anomalies=%d\n", res.RTPAnomalies)
	fmt.Fprintf(stdout, "collisions=%d\n", res.Collisions)
	fmt.Fprintf(stdout, "drift_ppm=%.3f\n", res.DriftPPM)
	if len(res.Faults) > 0 {
		kinds := make([]string, 0, len(res.Faults))
		for _, k := range res.Faults {
			kinds = append(kinds, string(k))
		}
		fmt.Fprintf(stdout, "faults=%s\n", strings.Join(kinds, ","))
	}
	if len(res.Notes) > 0 {
		fmt.Fprintf(stdout, "notes=%d\n", len(res.Notes))
	}
	for _, note := range res.Notes {
		fmt.Fprintf(stdout, "note=%s\n", note)
	}
	if runErr != nil {
		fmt.Fprintf(stdout, "recovered=%t\n", res.Recovered)
	}
	_ = mediaerr.Classify(runErr)
}
