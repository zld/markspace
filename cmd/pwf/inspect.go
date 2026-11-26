package main

import (
	"fmt"
	"os"

	"pwf/internal/media"
	"pwf/internal/pipeline"
	"pwf/internal/quality"
)

// runInspect parses and plans a single local source, mapping the unified
// error model onto the contract's exit codes: corrupt/truncated material
// is 3, an infeasible plan or other task failure is 4, bad arguments 2.
func runInspect(args []string) int {
	var work, rtpPath, mp4Path, codec string
	var clock int
	tightBudget := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--work":
			i++
			if i >= len(args) {
				return inspectUsage("--work requires a value")
			}
			work = args[i]
		case "--rtp":
			i++
			if i >= len(args) {
				return inspectUsage("--rtp requires a value")
			}
			rtpPath = args[i]
		case "--mp4":
			i++
			if i >= len(args) {
				return inspectUsage("--mp4 requires a value")
			}
			mp4Path = args[i]
		case "--codec":
			i++
			if i >= len(args) {
				return inspectUsage("--codec requires a value")
			}
			codec = args[i]
		case "--clock":
			i++
			if i >= len(args) {
				return inspectUsage("--clock requires a value")
			}
			if _, err := fmt.Sscanf(args[i], "%d", &clock); err != nil || clock <= 0 {
				return inspectUsage("--clock must be a positive integer")
			}
		case "--tight-budget":
			tightBudget = true
		default:
			return inspectUsage("unknown flag " + args[i])
		}
	}
	if work == "" {
		return inspectUsage("--work is required")
	}
	if err := validateWorkDir(work); err != nil {
		fmt.Fprintln(os.Stderr, "work dir rejected:", err)
		return exitUsage
	}

	src, err := buildInspectSource(rtpPath, mp4Path, codec, clock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "argument error:", err)
		return exitUsage
	}

	thresholds := quality.DefaultThresholds()
	if tightBudget {
		thresholds = quality.TightBudget()
	}
	out, runErr := pipeline.Run(pipeline.Options{
		WorkDir: work, Sources: []pipeline.Source{src},
		Thresholds: thresholds, Workers: 1,
	})
	if runErr != nil {
		return classifyError(runErr)
	}
	for _, m := range out.Measurements {
		fmt.Printf("track=%s codec=%s packets=%d bitrate_bps=%d max_gap=%d anomaly=%s\n",
			m.Track, m.Codec, m.PacketCount, m.AvgBitrateBps, m.MaxGap, m.AnomalyRatio)
	}
	if out.Plan != nil {
		fmt.Print(out.Plan.Summary())
	}
	return exitOK
}

func buildInspectSource(rtpPath, mp4Path, codec string, clock int) (pipeline.Source, error) {
	switch {
	case rtpPath != "" && mp4Path != "":
		return pipeline.Source{}, fmt.Errorf("specify only one of --rtp or --mp4")
	case rtpPath != "":
		if codec != "opus" && codec != "aac" {
			return pipeline.Source{}, fmt.Errorf("--codec must be opus or aac for RTP")
		}
		if clock == 0 {
			if codec == "opus" {
				clock = 48000
			} else {
				clock = 44100
			}
		}
		return pipeline.Source{
			Track: "inspect", Kind: pipeline.KindRTP, Path: rtpPath,
			Codec: codec, ClockHz: uint32(clock),
		}, nil
	case mp4Path != "":
		if codec == "" {
			codec = "aac"
		}
		return pipeline.Source{
			Track: "inspect", Kind: pipeline.KindMP4, Path: mp4Path, Codec: codec,
		}, nil
	default:
		return pipeline.Source{}, fmt.Errorf("one of --rtp or --mp4 is required")
	}
}

// classifyError maps media error kinds to the four exit codes.
func classifyError(err error) int {
	me, ok := media.AsError(err)
	if !ok {
		fmt.Fprintln(os.Stderr, "task failed:", err)
		return exitTask
	}
	switch me.Kind {
	case media.KindCorrupt, media.KindTruncated, media.KindBadLength,
		media.KindUndetermined, media.KindPathEscape:
		fmt.Fprintf(os.Stderr, "material error: %s\n", me.Error())
		return exitMaterial
	case media.KindPlanInfeasible:
		fmt.Fprintf(os.Stderr, "task failed (plan infeasible): %s\n", me.Error())
		return exitTask
	default:
		fmt.Fprintf(os.Stderr, "task failed: %s\n", me.Error())
		return exitTask
	}
}

func inspectUsage(msg string) int {
	fmt.Fprintln(os.Stderr, "argument error:", msg)
	return exitUsage
}
