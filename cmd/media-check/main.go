// Command media-check is the local acceptance entry point for the media
// processing kernel. It generates synthetic fixtures, runs the deterministic
// pipeline, and prints stable key=value reports.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: media-check demo --work DIR [--seed N] [--corrupt KIND] [--interrupt-after scan] [--resume]")
		return pipeline.ExitUsage
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	work := fs.String("work", "", "local output/work directory")
	corrupt := fs.String("corrupt", "", "corruption: truncated-box|missing-keyframe|timestamp-regression")
	interruptAfter := fs.String("interrupt-after", "", "stop after a complete stage (scan)")
	resume := fs.Bool("resume", false, "resume from the matching checkpoint")
	taskKey := fs.String("task-key", "", "optional stable task key override")
	if err := fs.Parse(args[1:]); err != nil {
		return pipeline.ExitUsage
	}
	if *work == "" {
		fmt.Fprintln(os.Stderr, "error: --work is required")
		return pipeline.ExitUsage
	}

	outcome, err := pipeline.Run(pipeline.Config{
		Seed:           *seed,
		Corrupt:        *corrupt,
		WorkDir:        *work,
		TaskKey:        *taskKey,
		InterruptAfter: *interruptAfter,
		Resume:         *resume,
	})
	if err != nil {
		printError(err)
		if outcome != nil && outcome.ExitCode != 0 {
			return outcome.ExitCode
		}
		return 1
	}
	printOutcome(outcome)
	return outcome.ExitCode
}

func printError(err error) {
	var fe *fault.Error
	if errors.As(err, &fe) {
		fmt.Printf("status=error\n")
		fmt.Printf("error_stage=%s\n", fe.Stage)
		fmt.Printf("error_code=%s\n", fe.Code)
		if fe.TrackID != 0 {
			fmt.Printf("track_id=%d\n", fe.TrackID)
		}
		if fe.Offset != 0 {
			fmt.Printf("byte_offset=%d\n", fe.Offset)
		}
		fmt.Printf("error=%q\n", fe.Message)
		return
	}
	fmt.Printf("status=error\nerror=%q\n", err.Error())
}

func printOutcome(o *pipeline.Outcome) {
	if o == nil {
		fmt.Println("status=error")
		return
	}
	if o.Interrupted {
		fmt.Println("status=interrupted")
		fmt.Printf("task_key=%s\n", o.TaskKey)
		fmt.Println("checkpoint_stage=scan")
		fmt.Printf("resume_command=media-check demo --seed 17 --work %s --resume\n", o.WorkDir)
		return
	}
	if o.Resumed {
		fmt.Println("status=resumed")
		fmt.Printf("resume_from=%s\n", o.ResumeFrom)
	}

	m := o.Manifest
	if o.Damage != nil && o.Damage.DamageCount > 0 {
		d := o.Damage
		fmt.Println("status=damaged")
		fmt.Printf("task_key=%s\n", o.TaskKey)
		fmt.Printf("damage_count=%d\n", d.DamageCount)
		fmt.Printf("first_bad_offset=%d\n", d.FirstBadOffset)
		fmt.Printf("track_id=%d\n", d.TrackID)
		fmt.Printf("time_range=[%d,%d)ms\n", d.StartMS, d.EndMS)
		if len(d.Segments) > 0 {
			fmt.Printf("damage_kind=%s\n", d.Segments[0].Kind)
		}
		if o.ManifestSHA256 != "" {
			fmt.Printf("manifest_sha256=%s\n", o.ManifestSHA256)
		}
		if o.ManifestPath != "" {
			fmt.Printf("manifest=%s\n", o.ManifestPath)
		}
		return
	}

	fmt.Println("status=ok")
	fmt.Printf("task_key=%s\n", o.TaskKey)
	if m == nil {
		return
	}
	fmt.Printf("tracks=%d\n", m.Summary.Tracks)
	fmt.Printf("frames=%d\n", m.Summary.Frames)
	fmt.Printf("duplicate_groups=%d\n", m.Summary.DuplicateGroups)
	fmt.Printf("alignment_error_ms=%.3f\n", float64(m.Summary.AlignmentErrorMS))
	fmt.Printf("frame_sha256=%s\n", m.Summary.FrameSHA256)
	fmt.Printf("segment_sha256=%s\n", m.Summary.SegmentSHA256)
	fmt.Printf("pixel_min=%d\n", m.Summary.PixelMin)
	fmt.Printf("pixel_max=%d\n", m.Summary.PixelMax)
	fmt.Printf("pixel_mean=%.3f\n", m.Summary.PixelMean)
	fmt.Printf("peak_dbfs=%.3f\n", m.Summary.PeakDBFS)
	fmt.Printf("manifest_sha256=%s\n", o.ManifestSHA256)
	fmt.Printf("manifest=%s\n", o.ManifestPath)
}
