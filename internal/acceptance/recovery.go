package acceptance

import (
	"fmt"
	"io"
	"os"

	"mediarepair/internal/batch"
	"mediarepair/internal/cache"
	"mediarepair/internal/media"
	"mediarepair/internal/testfixture"
)

func init() {
	RegisterScenario("interrupt", runInterruptScenario)
	RegisterScenario("resume", runResumeScenario)
}

func runInterruptScenario(opts Options, out io.Writer) int {
	if err := os.MkdirAll(opts.Root, 0o755); err != nil {
		fmt.Fprintf(out, "write_error=mkdir: %v\n", err)
		return ExitInputOrWrite
	}
	_ = resetRunState(opts.Root)
	if _, err := testfixture.CreateStandard(opts.Root); err != nil {
		PrintError(out, err)
		return ExitForError(err)
	}
	_, err := batch.Run(batch.Options{
		Root: opts.Root, BlockSize: opts.CheckpointFrames,
		InterruptAfterBlocks: opts.AfterBlock,
	})
	if media.HasCode(err, media.CodeInterrupt) {
		cp, _ := batch.LoadCheckpoint(opts.Root)
		committed := 0
		if cp != nil {
			committed = cp.CommittedBlocks
		}
		fmt.Fprintf(out, "scenario=interrupt status=checkpoint_preserved exit=75 after_block=%d committed_blocks=%d resume_hint=\"-scenario resume -root %s\"\n",
			opts.AfterBlock, committed, opts.Root)
		return ExitInterrupt
	}
	if err != nil {
		PrintError(out, err)
		return ExitForError(err)
	}
	fmt.Fprintln(out, "result_mismatch=expected interrupt exit 75 but run completed")
	return ExitUsage
}

func runResumeScenario(opts Options, out io.Writer) int {
	c := cache.New()
	first, err := batch.Run(batch.Options{Root: opts.Root, BlockSize: opts.CheckpointFrames, Cache: c})
	if err != nil {
		PrintError(out, err)
		return ExitForError(err)
	}
	second, err := batch.Run(batch.Options{Root: opts.Root, BlockSize: opts.CheckpointFrames, Cache: c})
	if err != nil {
		PrintError(out, err)
		return ExitForError(err)
	}
	if mismatch := checkResume(first, second); mismatch != "" {
		fmt.Fprintf(out, "result_mismatch=%s\n", mismatch)
		return ExitUsage
	}
	committed := 0
	complete := false
	if first.Checkpoint != nil {
		committed = first.Checkpoint.CommittedBlocks
		complete = first.Checkpoint.Complete
	}
	fmt.Fprintf(out, "scenario=resume status=ok exit=0 resumed=%v checkpoint_complete=%v committed_blocks=%d index_entries=%d segments=%d index_sha256=%s repeat_hash_match=true\n",
		first.Resumed, complete, committed, len(first.Index.Records), len(first.SegmentHashes), first.IndexSHA256)
	fmt.Fprintf(out, "frames=%d repaired=%d pixel_mean_rgb=%s", len(first.FrameHashes), first.Repair.RepairedCount, first.PixelMeanRGB)
	if first.Audio != nil {
		fmt.Fprintf(out, " audio_peak_dbfs=%.2f align_max_us=%.3f", first.Audio.PeakDBFS, first.AlignMaxUS)
	}
	fmt.Fprintln(out)
	for i, h := range first.FrameHashes {
		fmt.Fprintf(out, "frame_%04d_sha256=%s\n", i+1, h)
	}
	for i, h := range first.SegmentHashes {
		fmt.Fprintf(out, "segment_%03d_sha256=%s\n", i, h)
	}
	fmt.Fprintf(out, "cache_decode_hits=%d cache_decode_misses=%d index_invalidations=%d\n",
		second.CacheCounters.DecodeHits, second.CacheCounters.DecodeMisses, second.CacheCounters.IndexInvalidations)
	return ExitOK
}

func checkResume(first, second *batch.Result) string {
	if first.Index == nil || len(first.Index.Records) != 12 {
		return "resumed index is incomplete"
	}
	if len(first.SegmentHashes) != 3 {
		return "resumed segment count is not 3"
	}
	if !validSHA(first.IndexSHA256) || first.IndexSHA256 != second.IndexSHA256 {
		return "resumed index hash invalid or unstable"
	}
	for _, h := range first.SegmentHashes {
		if !validSHA(h) {
			return "resumed segment hash invalid"
		}
	}
	return ""
}

func interruptCoverage(root string, afterBlock int, out io.Writer) bool {
	opts := Options{Root: root, CheckpointFrames: 4, AfterBlock: afterBlock}
	code := runInterruptScenario(opts, out)
	return code == ExitInterrupt
}

func resumeCoverage(root string, out io.Writer) bool {
	code := runResumeScenario(Options{Root: root, CheckpointFrames: 4}, out)
	return code == ExitOK
}
