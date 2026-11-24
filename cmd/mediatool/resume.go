package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediatool/internal/fault"
	"mediatool/internal/fixture"
	"mediatool/internal/segment"
	"mediatool/internal/stream"
)

// runResume demonstrates checkpoint behavior: it runs a capture to completion
// with an interruption, then resumes. With --corrupt-checkpoint it deliberately
// binds the checkpoint to a different input, which must be refused with exit
// code 4. With --tamper-index the same digest-mismatch path is exercised.
func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "fixture seed")
	tracks := fs.Int("tracks", 4, "track count")
	bitrates := fs.Int("bitrates", 3, "representation ladder depth")
	injectLoss := fs.Int("inject-loss", 0, "packets to drop")
	injectTrunc := fs.Int("inject-truncation", 0, "datagrams to truncate")
	interruptAfter := fs.Int("interrupt-after", 2, "stop after N committed segments")
	corrupt := fs.Bool("corrupt-checkpoint", false, "rebind the checkpoint to a different input")
	workDir := fs.String("workdir", "", "output directory")
	keep := fs.Bool("keep", false, "keep the work directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir := *workDir
	if dir == "" {
		d, err := os.MkdirTemp("", "mediatool-resume-*")
		if err != nil {
			return err
		}
		dir = d
	}
	defer func() {
		if !*keep && *workDir == "" {
			os.RemoveAll(dir)
		}
	}()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	opts := fixture.DemoOptions(*seed, *tracks, *bitrates, *injectLoss, *injectTrunc)
	cap, err := fixture.Generate(opts)
	if err != nil {
		return err
	}
	scanRes, err := buildIndex(cap, opts)
	if err != nil {
		return err
	}
	reps, err := buildRepresentations(opts, *bitrates)
	if err != nil {
		return err
	}
	in := stream.Input{
		Digest:           scanRes.Index.InputDigest,
		InputBytes:       int64(len(cap.Bytes)),
		Index:            scanRes.Index,
		Reader:           bytes.NewReader(cap.Bytes),
		Representations:  reps,
		TruncatedOffsets: cap.TruncationOffsets,
	}
	checkpointPath := filepath.Join(dir, "checkpoint.json")
	cfg := stream.Config{
		Window:            256,
		MaxSegmentBytes:   1 << 20,
		CheckpointPath:    checkpointPath,
		OutputDir:         filepath.Join(dir, "segments"),
		StopAfterSegments: *interruptAfter,
		Segmenter:         segment.Config{DurationMs: opts.SegmentMs, KeyframeSearchMs: opts.SegmentMs},
	}

	first, err := stream.Execute(in, cfg)
	if err != nil {
		return err
	}
	fmt.Printf("interrupted stopped=%v committed=%d\n",
		first.Stopped, first.Manifest.Checkpoint.SegmentsCommitted)

	resumeCfg := cfg
	resumeCfg.ResumeFrom = checkpointPath
	resumeCfg.StopAfterSegments = 0

	if *corrupt {
		// Rebind the checkpoint to a foreign input digest so the resume must be
		// refused. The refusal itself is the expected outcome.
		cp, err := stream.LoadCheckpoint(checkpointPath, in.Digest)
		if err != nil {
			return err
		}
		cp.InputDigest = "0000000000000000000000000000000000000000000000000000000000000000"
		if err := stream.SaveCheckpoint(checkpointPath, cp); err != nil {
			return err
		}
		_, err = stream.Execute(in, resumeCfg)
		if err == nil {
			return fmt.Errorf("corrupted checkpoint was accepted")
		}
		if !fault.IsKind(err, fault.KindCheckpoint) {
			return fmt.Errorf("expected checkpoint refusal, got %v", err)
		}
		fmt.Printf("resume refused kind=checkpoint exit_code=%d\n", fault.ExitCode(err))
		return err
	}

	second, err := stream.Execute(in, resumeCfg)
	if err != nil {
		return err
	}
	fmt.Printf("resumed segments=%d fallbacks=%d\n", len(second.Manifest.Segments), second.Fallbacks)
	return nil
}
