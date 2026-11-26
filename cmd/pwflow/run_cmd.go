package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"pwflow/internal/fault"
	"pwflow/internal/pipeline"
	"pwflow/internal/validate"
)

func runPipelineCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("input", "", "input WebM/Matroska media")
	events := fs.String("events", "", "event JSONL timeline")
	rtpPath := fs.String("rtp", "", "framed RTP trace")
	out := fs.String("out", "", "output directory")
	checkpoint := fs.String("checkpoint", "", "checkpoint file path")
	budgetMiB := fs.Int64("budget-mib", 64, "memory budget in MiB")
	workers := fs.Int("workers", 4, "worker count")
	stopAfter := fs.Int("stop-after", 0, "stop after N newly completed segments")
	if err := fs.Parse(args); err != nil {
		return &cliError{code: exitRejected, err: err}
	}

	res, err := pipeline.Run(ctx, pipeline.Options{
		Input: *input, Events: *events, RTP: *rtpPath, Out: *out,
		Checkpoint: *checkpoint, BudgetMiB: *budgetMiB, Workers: *workers,
		StopAfter: *stopAfter,
	})
	if err != nil {
		if errors.Is(err, pipeline.ErrStoppedAfter) {
			completed := 0
			if res != nil && res.Checkpoint != nil {
				completed = len(res.Checkpoint.Completed)
			}
			return &cliError{code: exitInterrupted, err: fmt.Errorf(
				"checkpoint saved at %s after %d completed interval(s); rerun without --stop-after to resume",
				*checkpoint, completed)}
		}
		code := exitRejected
		if fault.IsCode(err, fault.BudgetExceeded) || fault.IsCode(err, fault.InvalidArgument) ||
			fault.IsCode(err, fault.InputTooLarge) || fault.IsCode(err, fault.CheckpointMismatch) ||
			fault.IsCode(err, fault.EBMLResyncFailed) {
			code = exitRejected
		}
		return &cliError{code: code, err: err}
	}
	if len(res.Resumed) > 0 {
		fmt.Printf("检查点恢复: 跳过已完成片段 %v，不重复提交\n", res.Resumed)
	}
	printManifestSummary(res.Manifest, res.ManifestSHA256)
	fmt.Printf("manifest: %s\n", res.ManifestPath)
	report, verr := validate.Verify(ctx, res.ManifestPath)
	if verr != nil {
		if report != nil {
			printVerification(report)
		}
		return &cliError{code: exitRejected, err: verr}
	}
	printVerification(report)
	if report.Status == pipeline.StatusGaps {
		return &cliError{code: exitGaps, err: fmt.Errorf("produced and verified with recoverable gaps")}
	}
	return nil
}
