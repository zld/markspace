package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	kernel "replaykernel/internal"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fixtureName := fs.String("fixture", "clean", "fixture to generate: clean, damaged, or malformed")
	out := fs.String("out", "./replay-out", "output directory")
	stopAfter := fs.String("stop-after", "", "stop after a recoverable stage (currently: cut)")
	resume := fs.Bool("resume", false, "resume from the cut checkpoint in --out")
	mutateSource := fs.Bool("mutate-source", false, "change the generated source digest")
	tamperOutput := fs.Bool("tamper-output", false, "tamper with a segment after manifest writing")
	verifyManifest := fs.Bool("verify-manifest", false, "validate an existing manifest in --out and exit")
	threshold := fs.Int("silence-threshold", 256, "maximum absolute PCM16 amplitude treated as silence")
	minSilenceMS := fs.Int64("min-silence-ms", 500, "minimum silence duration in milliseconds")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *verifyManifest {
		return runVerify(*out)
	}

	opts := kernel.RunOptions{
		Fixture:      *fixtureName,
		Out:          *out,
		StopAfter:    *stopAfter,
		Resume:       *resume,
		MutateSource: *mutateSource,
		TamperOutput: *tamperOutput,
		Silence: kernel.SilenceConfig{
			ThresholdAmplitude: *threshold,
			MinDurationMS:      *minSilenceMS,
		},
	}
	res, err := kernel.Run(opts)
	printReport(opts, res, err)
	if err == nil {
		return 0
	}
	var stop *kernel.CheckpointStop
	if errors.As(err, &stop) {
		return 2
	}
	return kernel.ExitCodeOf(err)
}

func runVerify(out string) int {
	m, rep, path, err := kernel.ValidateManifestFile(out)
	res := &kernel.RunResult{ManifestPath: path, Validation: rep}
	if m != nil {
		res.SourceDigest = m.Source.Digest
		res.Stages = m.Stages
		res.Wraps = m.Wraps
		res.CutCount = m.CutCount
		res.SilenceMS = m.SilenceMS
		res.GapEventCount = len(m.GapEvents)
		res.Manifest = m
	}
	printReport(kernel.RunOptions{Out: out}, res, err)
	if err != nil || !rep.Valid {
		return 1
	}
	return 0
}

func printReport(opts kernel.RunOptions, res *kernel.RunResult, err error) {
	if res == nil {
		res = &kernel.RunResult{}
	}
	fmt.Printf("source_digest=%s\n", dash(res.SourceDigest))
	fmt.Printf("fixture=%s mutated=%t\n", dash(opts.Fixture), opts.MutateSource)

	status := "success"
	if err != nil {
		status = "failed"
		var stop *kernel.CheckpointStop
		if errors.As(err, &stop) {
			status = "recoverable_interrupt"
		}
	}
	stages := strings.Join(res.Stages, ",")
	if stages == "" {
		stages = "-"
	}
	fmt.Printf("stage_status=%s stages=%s\n", status, stages)
	fmt.Printf("wraps=%d cuts=%d silence_ms=%d gap_events=%d input_packet=%d\n",
		res.Wraps, res.CutCount, res.SilenceMS, res.GapEventCount, res.InputPacket)

	if err != nil {
		fmt.Printf("error=%q exit_code=%d\n", err.Error(), kernel.ExitCodeOf(err))
	}

	printRecovery(opts, res, err)
	printDuplicateDiff(res)
	printManifestSummary(res)
}

func printRecovery(opts kernel.RunOptions, res *kernel.RunResult, err error) {
	checkpointValue := "-"
	var stop *kernel.CheckpointStop
	if errors.As(err, &stop) {
		checkpointValue = stop.Stage
	} else if opts.Resume {
		checkpointValue = kernel.StageCut
	}
	fmt.Printf("checkpoint=%s checkpoint_path=%s\n", checkpointValue, dash(res.CheckpointPath))
	fmt.Printf("resume_stage=%s resume_packet=%d\n", dash(res.ResumeStage), res.ResumePacket)
}

func printDuplicateDiff(res *kernel.RunResult) {
	fmt.Printf("duplicate_diff created=%d reused=%d invalidated=%d rtp_duplicates=%d content_duplicate_groups=%d\n",
		res.Cache.Created, res.Cache.Reused, res.Cache.Invalidated,
		res.RTPStats.Duplicates, res.DuplicateGroups)
	// Stable aliases used by terminal acceptance checks.
	fmt.Printf("created=%d reused=%d invalidated=%d\n",
		res.Cache.Created, res.Cache.Reused, res.Cache.Invalidated)
}

func printManifestSummary(res *kernel.RunResult) {
	if res.Manifest == nil {
		fmt.Println("manifest_status=not_written manifest_valid=n/a items=0 validation_errors=0")
		return
	}
	valid := "false"
	if res.Validation.Valid {
		valid = "true"
	}
	fmt.Printf("manifest_path=%s\n", dash(res.ManifestPath))
	fmt.Printf("manifest_status=written manifest_valid=%s items=%d validation_errors=%d\n",
		valid, len(res.Manifest.Segments), res.Validation.ErrorCount)
	for _, msg := range res.Validation.Errors {
		fmt.Printf("manifest_error=%s\n", msg)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
