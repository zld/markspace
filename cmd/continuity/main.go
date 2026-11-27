// Command continuity is the acceptance entry point for the media continuity
// processing kernel. It generates self-contained synthetic frame and RTP
// streams, runs the resumable pipeline, and prints the required metrics.
//
// Usage:
//
//	continuity demo   --out DIR [--interrupt-after N] [--corrupt checksum] [--case timestamp-overflow]
//	continuity resume --out DIR
//	continuity verify --out DIR
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"example.com/continuity/internal/engine"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/media"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := parseFlags(os.Args[2:])

	var code int
	switch cmd {
	case "demo":
		code = runDemo(args)
	case "resume":
		code = runResume(args)
	case "verify":
		code = runVerify(args)
	default:
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: continuity {demo|resume|verify} --out DIR [flags]")
}

type flags struct {
	out            string
	interruptAfter int
	corrupt        string
	testCase       string
}

func parseFlags(args []string) flags {
	var f flags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			if i+1 < len(args) {
				f.out = args[i+1]
				i++
			}
		case "--interrupt-after":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &f.interruptAfter)
				i++
			}
		case "--corrupt":
			if i+1 < len(args) {
				f.corrupt = args[i+1]
				i++
			}
		case "--case":
			if i+1 < len(args) {
				f.testCase = args[i+1]
				i++
			}
		}
	}
	return f
}

func requireOut(f flags) string {
	if f.out == "" {
		usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(f.out, 0o755); err != nil {
		fatal(fault.StagePipeline, err)
	}
	return f.out
}

func runDemo(f flags) int {
	out := requireOut(f)
	ctx := context.Background()
	spec := media.DefaultSpec()

	frameData, err := spec.WriteFrameStream()
	if err != nil {
		fatal(fault.StageFrame, err)
	}
	rtpData, err := spec.WriteRTPStream()
	if err != nil {
		fatal(fault.StageSilence, err)
	}

	// Fault-injection cases.
	switch {
	case f.testCase == "timestamp-overflow":
		frameData, err = media.WriteOverflowFrameStream()
		if err != nil {
			fatal(fault.StageFrame, err)
		}
	case f.corrupt == "checksum":
		frameData = media.CorruptChecksum(frameData)
	}

	cfg := engine.DefaultConfig()
	cfg.InterruptAfter = f.interruptAfter
	e := engine.New(cfg, out)
	res, err := e.Run(ctx, frameData, rtpData)
	if errors.Is(err, engine.ErrInterrupted) {
		fmt.Printf("interrupted after %d committed frames; checkpoint saved to %s\n",
			f.interruptAfter, out)
		return 0
	}
	if err != nil {
		fatal(fault.AsError(err).Stage, err)
	}
	printMetrics(res)
	return 0
}

func runResume(f flags) int {
	out := requireOut(f)
	spec := media.DefaultSpec()
	frameData, err := spec.WriteFrameStream()
	if err != nil {
		fatal(fault.StageFrame, err)
	}
	rtpData, err := spec.WriteRTPStream()
	if err != nil {
		fatal(fault.StageSilence, err)
	}
	e := engine.New(engine.DefaultConfig(), out)
	res, err := e.Run(context.Background(), frameData, rtpData)
	if err != nil {
		fatal(fault.AsError(err).Stage, err)
	}
	if !res.Resumed {
		fmt.Fprintln(os.Stderr, "warning: no committed checkpoint found; ran from start")
	}
	printMetrics(res)
	return 0
}

func runVerify(f flags) int {
	out := requireOut(f)
	e := engine.New(engine.DefaultConfig(), out)
	report, err := e.Verify(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
		return 1
	}
	fmt.Printf("verify ok: frames=%d summary_identical=%t manifests_identical=%t thresholds_met=%t\n",
		report.FrameCount, report.SummaryIdentical, report.AllManifestsSame, report.ThresholdsMet)
	return 0
}

func printMetrics(res *engine.Result) {
	s := res.Summary
	fmt.Printf("target_bitrate=%.2f\n", s.TargetBitrate)
	fmt.Printf("actual_bitrate=%.2f\n", s.ActualBitrate)
	fmt.Printf("bitrate_error_pct=%.4f\n", s.BitrateErrorPct)
	fmt.Printf("psnr_db=%.4f\n", s.PSNRDB)
	fmt.Printf("ssim=%.6f\n", s.SSIM)
	fmt.Printf("manifest_alignment_ns=%d\n", s.ManifestAlignNS)
	fmt.Printf("frames=%d\n", s.Frames)
	fmt.Printf("segments=%d\n", s.Segments)
	fmt.Printf("silence_ns=%d\n", s.SilenceNS)
	fmt.Printf("duplicates=%d\n", s.Duplicates)
}

func fatal(stage string, err error) {
	fe := fault.AsError(err)
	if fe != nil {
		fmt.Fprintf(os.Stderr, "error stage=%s frame=%d offset=%d: %v\n",
			fe.Stage, fe.Frame, fe.Offset, fe.Err)
	} else {
		fmt.Fprintf(os.Stderr, "error stage=%s: %v\n", stage, err)
	}
	os.Exit(1)
}
