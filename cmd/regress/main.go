// Command regress is the offline weak-network RTP/WAV regression entry
// point. It generates all fixtures locally, runs the staged pipeline and
// prints quantified results. Exit codes: 0 success, 1 input/validation
// failure, 2 parameter error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediakernel/internal/clock"
	"mediakernel/internal/pipeline"
	"mediakernel/internal/report"
)

const shaPrefixLen = 16

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 2
	}
	if args[0] != "demo" {
		fmt.Fprintf(os.Stderr, "error: unknown command %q (expected \"demo\")\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	channels := fs.Uint("channels", 2, "audio channel count (1,2,4,6,8)")
	rate := fs.Uint("rate", 48000, "sample rate and RTP clock rate in Hz")
	frames := fs.Int("frames", 96000, "number of sample frames to generate")
	fault := fs.String("fault", "none", "injected fault: none|truncate|checkpoint")
	resume := fs.Bool("resume", false, "resume from an existing checkpoint")
	workDir := fs.String("workdir", "", "explicit local working directory (default ./.regress)")
	blockFrames := fs.Int64("block-frames", 480, "frames per sparse index block")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *channels == 0 || *channels > 65535 || *rate == 0 || *rate > 4294967295 ||
		*frames <= 0 || *blockFrames <= 0 {
		fmt.Fprintf(os.Stderr, "error: channels/rate/frames/block-frames must be positive and in range\n")
		return 2
	}

	base, err := resolveBase(*workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	o := pipeline.Options{
		BaseDir:         base,
		WAVRelPath:      filepath.Join("input", "sample.wav"),
		OutputDir:       filepath.Join("output", stringFault(*fault)),
		Channels:        uint16(*channels),
		SampleRate:      uint32(*rate),
		Frames:          *frames,
		BlockFrames:     *blockFrames,
		Fault:           pipeline.Fault(*fault),
		CrashAfterStage: "index",
	}

	result, runErr := pipeline.Run(o, *resume)
	if runErr != nil {
		if errors.Is(runErr, pipeline.ErrCrashRehearsal) {
			// A non-resume checkpoint rehearsal is an expected intermediate
			// state; guide the operator to the resume command and exit 0 here
			// only when --resume was not requested.
			fmt.Println("checkpoint fault injected after index stage; rerun with --resume")
			return 0
		}
		return emitFailure(o, runErr)
	}

	rep := report.Build(report.Options{
		Frames:          result.Frames,
		IndexEntries:    result.IndexEntries,
		LocatingErrors:  result.LocatingErrors,
		Resume:          result.Resume,
		ResumeMillis:    result.ResumeMillis,
		ReusedStages:    result.ReusedStages,
		InputDigest:     result.InputDigest,
		IndexDigest:     result.IndexDigest,
		OutputSHA256:    result.OutputSHA256,
		FirstByteOffset: result.FirstByteOffset,
	})
	printSummary(rep)
	return 0
}

func emitFailure(o pipeline.Options, err error) int {
	ce, ok := clock.AsError(err)
	if ok && (ce.Category == clock.CatInvalidParam || ce.Category == clock.CatSymlinkEscape) {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	// Input/validation failure: emit the located byte offset and exit 1.
	rep := report.FromError(report.Options{
		Frames: int64(o.Frames), FirstByteOffset: -1,
	}, err)
	printSummary(rep)
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return 1
}

func printSummary(r *report.Report) {
	prefix := r.OutputSHA256
	if len(prefix) > shaPrefixLen {
		prefix = prefix[:shaPrefixLen]
	}
	fmt.Printf("frames=%d\n", r.Frames)
	fmt.Printf("定位错误数=%d\n", r.LocatingErrors)
	fmt.Printf("output_sha256=%s\n", prefix)
	if r.FirstByteOffset >= 0 {
		fmt.Printf("byte_offset=%d\n", r.FirstByteOffset)
	}
	if r.Resume {
		fmt.Printf("resume=true\n")
		fmt.Printf("resume_ms=%d\n", r.ResumeMillis)
	}
}

func resolveBase(flagValue string) (string, error) {
	base := flagValue
	if base == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		base = filepath.Join(cwd, ".regress")
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
}

func stringFault(f string) string {
	switch pipeline.Fault(f) {
	case pipeline.FaultNone:
		return "none"
	case pipeline.FaultTruncate:
		return "truncate"
	case pipeline.FaultCheckpoint:
		return "checkpoint"
	default:
		return "unknown"
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr,
		"usage: regress demo --channels N --rate HZ --frames F --fault none|truncate|checkpoint [--resume]")
}
