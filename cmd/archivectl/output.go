package main

import (
	"fmt"
	"os"
	"path/filepath"

	"archivectl/internal/fixture"
	"archivectl/internal/pipeline"
)

// attachAndWrite measures pipeline P95, writes frames, manifest and fixtures.
func attachAndWrite(res *pipeline.Result, set *fixture.Set, seed int64,
	lossWindow int, out string) error {
	if err := res.AttachPerformance(seed, lossWindow, 100); err != nil {
		return err
	}
	frameDir := filepath.Join(out, "frames")
	if err := os.MkdirAll(frameDir, 0o755); err != nil {
		return err
	}
	for _, f := range res.Frames {
		name := fmt.Sprintf("frame_%04d.png", f.Index)
		if err := os.WriteFile(filepath.Join(frameDir, name), f.PNG, 0o644); err != nil {
			return err
		}
	}

	manifest := filepath.Join(out, "frame_manifest.sha256")
	var lines []byte
	for _, h := range res.FrameHashes {
		lines = append(lines,
			fmt.Sprintf("%s  frame_%04d.png  %dns\n", h.SHA256, h.Index, h.TimeNs)...)
	}
	lines = append(lines, fmt.Sprintf("# manifest_sha256 %s\n", res.ManifestSHA)...)
	if err := os.WriteFile(manifest, lines, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest_boxes.mp4"),
		res.ManifestBox, 0o644); err != nil {
		return err
	}

	// Persist the generated fixtures for repeatable offline inspection.
	files := map[string][]byte{
		fixture.NormalInterleave: set.Normal,
		fixture.UnknownLength:    set.Unknown,
		fixture.TailTruncated:    set.Truncated,
		fixture.CorruptCluster:   set.Corrupt,
		fixture.IncompleteIndex:  set.Incomplete,
		fixture.RTPCapture:       set.RTP,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// printSummary emits the stable, machine-greppable metrics contract.
func printSummary(res *pipeline.Result, set *fixture.Set) {
	fmt.Println("archivectl recovery summary")
	fmt.Printf("seed=%d loss_window=%d\n", set.Seed, set.LossWindow)
	fmt.Printf("av_sync_error_ms=%.3f\n", res.AVSyncP95Ms)
	fmt.Printf("packet_recovery_rate=%.4f\n", res.RTP.RecoveryRate())
	fmt.Printf("pipeline_p95_ms=%.3f\n", res.PipelineP95Ms)
	fmt.Printf("frames=%d\n", len(res.Frames))
	fmt.Printf("index_rebuilt=%t\n", res.Index.Rebuilt)
	fmt.Printf("frame_sha256=%s\n", res.ManifestSHA)
	fmt.Printf("error_count=%d\n", len(res.Errors))
	fmt.Printf("samples=%d index_entries=%d damaged_offsets=%d\n",
		res.SampleCount, res.Index.Count(), len(res.Index.DamagedBytes))
	fmt.Printf("rtp_expected=%d rtp_recovered=%d rtp_unrecoverable=%d rtp_duplicates=%d\n",
		res.RTP.Expected, res.RTP.Recovered, res.RTP.Unrecoverable,
		res.RTP.Duplicates)
	for i, a := range res.Audio {
		fmt.Printf("audio_track_%d samples=%d silent_runs=%d range_ns=%d..%d\n",
			i, a.SampleCount, a.SilentRuns, a.StartNs, a.EndNs)
	}
	for _, e := range res.Errors {
		fmt.Printf("error stage=%s category=%s offset=%d\n",
			e.Stage, e.Category, e.Offset)
	}
}
