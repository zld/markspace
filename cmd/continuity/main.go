// Command continuity is the terminal entry point for the resumable offline
// media processing kernel. Subcommands:
//
//	continuity fixture --out DIR [--case ok|truncated-mdat|...]
//	continuity run     --input DIR --index DIR --out DIR [--chunk N]
//	                     [--crash-after-write N] [--resume]
//	continuity verify  --input DIR --index DIR --out DIR
//
// Exit codes: 0 success, 2 invalid input (nothing published), 75 injected
// interruption.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"continuity/internal/clock"
	"continuity/internal/fixture"
	"continuity/internal/index"
	"continuity/internal/pipeline"
)

const (
	exitOK          = 0
	exitInvalid     = 2
	exitInjected    = 75
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitInvalid)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "fixture":
		err = runFixture(args)
	case "run":
		err = runPipeline(args, false)
	case "verify":
		err = runPipeline(args, true)
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", cmd)
		usage()
		os.Exit(exitInvalid)
	}
	if err == nil {
		os.Exit(exitOK)
	}
	var injected *index.InjectedCrash
	if errors.As(err, &injected) {
		fmt.Fprintf(os.Stderr, "%s\n", injected.Error())
		os.Exit(exitInjected)
	}
	me := clock.AsMedia(err)
	if me != nil {
		fmt.Fprintf(os.Stderr, "%s\n", me.Error())
	} else {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	os.Exit(exitInvalid)
}

func runFixture(args []string) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (required)")
	kind := fs.String("case", fixture.CaseOK, "fixture case")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return clock.New(clock.KindInvalidInput, "fixture", "--out is required")
	}
	g, err := fixture.New(*out)
	if err != nil {
		return err
	}
	if *kind == fixture.CaseOK {
		err = g.Generate()
	} else {
		err = g.GenerateBad(*kind)
	}
	if err != nil {
		return err
	}
	fmt.Printf("fixture %q written to %s\n", *kind, *out)
	return nil
}

func runPipeline(args []string, verifyOnly bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("input", "", "fixture input directory (required)")
	indexDir := fs.String("index", "", "journal/index directory (required)")
	out := fs.String("out", "", "output directory (required)")
	chunk := fs.Int("chunk", 2, "checkpoint cadence in segments")
	crash := fs.Int("crash-after-write", 0,
		"inject an exit after the Nth artifact write, before state commit")
	resume := fs.Bool("resume", false, "resume from the last committed checkpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *indexDir == "" || *out == "" {
		return clock.New(clock.KindInvalidInput, "pipeline",
			"--input, --index and --out are required")
	}
	r, err := pipeline.NewRunner(pipeline.Config{
		InputDir:        *input,
		IndexDir:        *indexDir,
		OutputDir:       *out,
		Chunk:           *chunk,
		CrashAfterWrite: *crash,
		Resume:          *resume,
	})
	if err != nil {
		return err
	}
	var rep *pipeline.Report
	if verifyOnly {
		rep, err = r.Verify()
	} else {
		rep, err = r.Run()
	}
	if err != nil {
		return err
	}
	printReport(rep)
	return nil
}

func printReport(rep *pipeline.Report) {
	fmt.Println("representation target_bps actual_bps deviation_pct " +
		"PSNR_dB max_align_ms")
	for _, row := range rep.Representations {
		fmt.Printf("%-14s %10d %10d %13.4f %8.2f %12.4f\n",
			row.RepID, row.TargetBPS, row.ActualBPS,
			row.DeviationPct, row.PSNRDB, row.MaxAlignMS)
	}
	fmt.Printf("recovered=%d duplicate_publish=%d\n",
		rep.Recovered, rep.DuplicatePublish)
	fmt.Printf("manifest_sha256=%s\n", rep.ManifestSHA256)
}

func usage() {
	fmt.Fprint(os.Stderr, `continuity - resumable offline media continuity kernel

usage:
  continuity fixture --out DIR [--case ok|truncated-mdat|duplicate-dash|hls-discontinuity-gap|rtp-wrap]
  continuity run     --input DIR --index DIR --out DIR [--chunk N] [--crash-after-write N] [--resume]
  continuity verify  --input DIR --index DIR --out DIR

exit codes: 0 success | 2 invalid input, nothing published | 75 injected interruption
`)
}
