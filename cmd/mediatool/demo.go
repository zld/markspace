package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediatool/internal/fixture"
	"mediatool/internal/mapping"
	"mediatool/internal/metrics"
	"mediatool/internal/pipeline"
	"mediatool/internal/remux"
)

// DemoConfig holds the parsed demo arguments.
type DemoConfig struct {
	Seed      int
	Fragments int
	LateAt    int
	OutDir    string
}

// runDemo generates a dataset, pushes it through the full pipeline and
// prints the required summary keys.
func runDemo(args []string) int {
	cfg, err := parseDemoArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return 2
	}
	if cfg.Fragments < 2 {
		fmt.Fprintf(os.Stderr, "argument error: --fragments must be >= 2\n")
		return 2
	}
	if cfg.LateAt >= cfg.Fragments {
		fmt.Fprintf(os.Stderr, "argument error: --late-at must be < --fragments\n")
		return 2
	}

	group := fixture.Generate(int64(cfg.Seed), cfg.Fragments, cfg.LateAt)
	rules := []mapping.Rule{
		{OutputSlot: "video-main", TrackID: 1, MediaType: "video", CodecKey: "avc1"},
		{OutputSlot: "audio-main", TrackID: 2, MediaType: "audio", CodecKey: "mp4a"},
	}
	pipe, err := pipeline.New(rules, cfg.OutDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "processing failure: %v\n", err)
		return 1
	}
	pipe.SetTracks(group.Tracks)

	// Deliver audio first, then video with the late fragment reordered;
	// this exercises interleaved arrival and the watermark backtrack.
	videoOrder := fixture.ReorderLate(group.VideoFrags, cfg.LateAt)
	n := len(group.AudioFrags)
	if len(videoOrder) > n {
		n = len(videoOrder)
	}
	for i := 0; i < n; i++ {
		if i < len(group.AudioFrags) {
			pipe.Ingest(pipeline.Input{Data: group.AudioFrags[i].Bytes})
		}
		if i < len(videoOrder) {
			f := videoOrder[i]
			pkt := group.RTPVideo[i%len(group.RTPVideo)]
			buf, merr := pkt.Marshal()
			if merr != nil {
				fmt.Fprintf(os.Stderr, "processing failure: %v\n", merr)
				return 1
			}
			_, raw, perr := parseRTPTimestamp(buf)
			if perr != nil {
				fmt.Fprintf(os.Stderr, "processing failure: %v\n", perr)
				return 1
			}
			pipe.Ingest(pipeline.Input{
				Data: f.Bytes, HasRTP: true,
				RTPWallMS: int64(i) * fixture.GridMS, RTPMedia: uint64(raw),
			})
		}
	}
	// Inject the parser-rejection fixtures so rejected_boxes is non-zero.
	for _, c := range group.Corrupted {
		pipe.Ingest(pipeline.Input{Data: c.Bytes})
	}

	result, err := pipe.Finalize()
	if err != nil {
		fmt.Fprintf(os.Stderr, "processing failure: %v\n", err)
		return 1
	}
	report := metrics.Compute(result.Manifest, result.Events, []metrics.SlotTarget{
		{Slot: "video-main", TargetBitBPS: group.VideoTargetBPS},
		{Slot: "audio-main", TargetBitBPS: group.AudioTargetBPS},
	})

	printDemoSummary(cfg, result, report)
	if err := writeSummaryFile(result, report); err != nil {
		fmt.Fprintf(os.Stderr, "processing failure: %v\n", err)
		return 1
	}
	return 0
}

func parseDemoArgs(args []string) (DemoConfig, error) {
	cfg := DemoConfig{Seed: 42, Fragments: 12, LateAt: 7, OutDir: ""}
	var err error
	cfg.Seed, args, err = flagInt(args, "--seed", 42)
	if err != nil {
		return cfg, err
	}
	cfg.Fragments, args, err = flagInt(args, "--fragments", 12)
	if err != nil {
		return cfg, err
	}
	cfg.LateAt, args, err = flagInt(args, "--late-at", 7)
	if err != nil {
		return cfg, err
	}
	cfg.OutDir, args, err = flagString(args, "--out", "")
	if err != nil {
		return cfg, err
	}
	if cfg.OutDir == "" {
		return cfg, fmt.Errorf("--out is required")
	}
	for _, a := range args {
		return cfg, fmt.Errorf("unexpected argument %q", a)
	}
	return cfg, nil
}

func printDemoSummary(cfg DemoConfig, result *pipeline.Result, r metrics.Report) {
	fmt.Printf("target_bitrate_bps=%d\n", r.TargetBitrateBPS)
	fmt.Printf("actual_bitrate_bps=%d\n", r.ActualBitrateBPS)
	fmt.Printf("bitrate_deviation_pct=%.2f\n", r.BitrateDeviationPct)
	fmt.Printf("sample_hash_match=%t\n", r.SampleHashMatch)
	fmt.Printf("quality_samples=%d\n", r.QualitySamples)
	fmt.Printf("manifest_alignment_ms=%.2f\n", r.ManifestAlignmentMS)
	fmt.Printf("late_recomputed=%d\n", result.Counters.LateRecomputed)
	fmt.Printf("rejected_boxes=%d\n", result.Counters.RejectedBoxes)
	fmt.Printf("output_dir=%s\n", cfg.OutDir)
}

// writeSummaryFile stores a machine-readable copy next to the manifest so
// the verify subcommand can recompute and compare it deterministically.
func writeSummaryFile(result *pipeline.Result, r metrics.Report) error {
	dir := result.OutputDir
	summaryPath, err := remux.SafePath(dir, "summary.txt")
	if err != nil {
		return err
	}
	content := fmt.Sprintf(
		"target_bitrate_bps=%d\nactual_bitrate_bps=%d\nbitrate_deviation_pct=%.2f\n"+
			"sample_hash_match=%t\nquality_samples=%d\nmanifest_alignment_ms=%.2f\n"+
			"late_recomputed=%d\nrejected_boxes=%d\n",
		r.TargetBitrateBPS, r.ActualBitrateBPS, r.BitrateDeviationPct,
		r.SampleHashMatch, r.QualitySamples, r.ManifestAlignmentMS,
		result.Counters.LateRecomputed, result.Counters.RejectedBoxes)
	if err := os.MkdirAll(filepath.Dir(summaryPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(summaryPath, []byte(content), 0o644)
}
