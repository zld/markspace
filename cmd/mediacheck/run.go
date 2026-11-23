package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediaforge/internal/mediaerr"
	"mediaforge/internal/pipeline"
	"mediaforge/internal/quality"
)

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	input := fs.String("input", "", "input capture file")
	out := fs.String("out", "", "output directory")
	sampleRate := fs.Int64("sample-rate", 48000, "output sample rate")
	channels := fs.Int("channels", 2, "output channel count (1 or 2)")
	target := fs.Float64("target-lufs", -23, "target integrated loudness")
	clockRate := fs.Int64("clock-rate", 0, "RTP timestamp rate (defaults to sample rate)")
	payloadLayout := fs.Int("payload-channels", 0, "channels in each RTP payload")
	delay := fs.Int("delay-ms", 40, "watermark reorder delay in milliseconds")
	ceiling := fs.Float64("ceiling-dbfs", -1, "sample-peak limiter ceiling")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *input == "" || *out == "" {
		return usageError("run requires --input and --out")
	}
	opts := pipeline.Options{
		InputPath: *input, OutDir: *out, SampleRate: *sampleRate,
		Channels: *channels, TargetLUFS: *target, ClockRate: *clockRate,
		PayloadLayout: *payloadLayout, DelayMs: *delay, CeilingDBFS: *ceiling,
	}
	res, err := pipeline.Run(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediacheck: %v\n", err)
		if mediaerr.CodeOf(err) == mediaerr.CodeParam {
			return 2
		}
		return 1
	}
	switch err := res.Stats.Validate(res.Stats.TargetBitrateBps, true); err {
	case nil:
	default:
		fmt.Fprintf(os.Stderr, "mediacheck: verification failed: %v\n", err)
		fmt.Println(res.Stats.Summary())
		return 1
	}
	fmt.Println(res.Stats.Summary())
	fmt.Printf("wrote %s\n", res.MP4Path)
	fmt.Printf("wrote %s\n", res.ManifestPath)
	fmt.Printf("wrote %s\n", res.StatsPath)
	return 0
}

func cmdVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "directory produced by run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		return usageError("verify requires --dir")
	}
	stats, err := readStats(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediacheck: %v\n", err)
		return 1
	}
	if err := verifyDir(*dir, stats); err != nil {
		fmt.Fprintf(os.Stderr, "mediacheck: verification failed: %v\n", err)
		return 1
	}
	fmt.Printf("verified %s\n", *dir)
	fmt.Println(stats.Summary())
	return 0
}

func readStats(dir string) (quality.Stats, error) {
	var s quality.Stats
	matches, err := filepath.Glob(filepath.Join(dir, "*.stats.json"))
	if err != nil || len(matches) != 1 {
		return s, fmt.Errorf("expected one *.stats.json in %s", dir)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", matches[0], err)
	}
	return s, nil
}
