// Command mediacheck is a terminal-only Matroska/WebM inspection and remux
// tool. It never opens a window, browser, camera or microphone.
//
// Usage:
//
//	mediacheck scan    <input.mkv> [--index-dir DIR]
//	mediacheck process <input.mkv> --index-dir DIR --output OUT.mp4 [flags]
//	mediacheck demo     --seed N --frames N [--loss F --jitter-ms N]
//	                     [--truncate cluster] [--mutate-audio-layout]
//
// Exit codes: 0 = completed and within limits; 2 = corrupt/unrecoverable
// input; 1 = bad parameters or runtime failure.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediacheck/audio"
	"mediacheck/color"
	"mediacheck/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/pipeline"
	"mediacheck/timeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "scan":
		err = runScan(os.Args[2:])
	case "process":
		err = runProcess(os.Args[2:])
	case "demo":
		err = runDemo(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
	if err != nil {
		exitWith(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediacheck - terminal Matroska/WebM inspection and remux

  scan    <input> [--index-dir DIR]
  process <input> --index-dir DIR --output OUT.mp4 [--frames N]
  demo    --seed N --frames N [--loss F] [--jitter-ms N]
          [--truncate cluster] [--mutate-audio-layout]
          [--index-dir DIR] [--output OUT.mp4]

Exit codes: 0 ok, 1 parameter/runtime error, 2 corrupt or unrecoverable input
`)
}

// exitWith maps the shared error model to the documented process exit codes.
func exitWith(err error) {
	switch {
	case timeline.IsCategory(err, timeline.CatCorrupt),
		timeline.IsCategory(err, timeline.CatUnsupported),
		timeline.IsCategory(err, timeline.CatUnrecoverable):
		fmt.Fprintf(os.Stderr, "input fault: %v\n", err)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type commonFlags struct {
	indexDir string
	output   string
	frames   int
}

func parseCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.indexDir, "index-dir", "", "directory for summary-bound index files")
	fs.StringVar(&c.output, "output", "", "final remuxed MP4 path")
	fs.IntVar(&c.frames, "frames", 64, "frame count / render sample count")
	return c
}

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	parseCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return timeline.New(timeline.CatConfig, "scan requires an input path")
	}
	in, err := openInput(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()
	res, scanErr := ebml.Scan(in)
	summary := map[string]interface{}{
		"tracks":       len(res.Tracks),
		"blocks":       res.BlockCount,
		"segment_live": res.SegmentLive,
		"truncated_at": res.TruncatedAt,
		"crc_failures": res.CheckFailures,
	}
	printJSON(summary)
	return scanErr
}

func runProcess(args []string) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	c := parseCommon(fs)
	seed := fs.Int64("seed", 1, "fixture/rng seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return timeline.New(timeline.CatConfig, "process requires an input path")
	}
	in, err := openInput(fs.Arg(0))
	if err != nil {
		return err
	}
	defer in.Close()
	cfg := buildConfig(*c)
	cfg.Frames = c.frames
	m, err := pipeline.Run(context.Background(), in, *seed, cfg)
	if err != nil {
		return err
	}
	printMetrics(m)
	// Corrupt/unrecoverable conditions are reported via metrics, not a Go
	// error, so map them to exit code 2 here.
	if !m.Published || len(m.Faults) > 0 {
		return timeline.New(timeline.CatUnrecoverable,
			fmt.Sprintf("run did not publish a complete result: %d fault(s)", len(m.Faults)))
	}
	return nil
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	c := parseCommon(fs)
	seed := fs.Int64("seed", 17, "deterministic rng seed")
	loss := fs.Float64("loss", 0.0, "simulated RTP loss rate")
	jitter := fs.Int("jitter-ms", 0, "simulated return-feed jitter")
	truncate := fs.String("truncate", "", "corruption: truncate at 'cluster'")
	badLayout := fs.Bool("mutate-audio-layout", false, "inject a duplicate channel layout")
	colorConflict := fs.Bool("color-conflict", false, "inject conflicting colour metadata")
	unknownColor := fs.Bool("unknown-color", false, "inject an unknown colour enumeration")
	hardFraction := fs.Float64("hard-loss", 0.08, "fraction of loss that never recovers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c.indexDir == "" {
		c.indexDir = filepath.Join(os.TempDir(), "mediacheck-idx")
	}

	stream, err := fixture.Build(fixture.Config{
		Seed: *seed, Frames: c.frames,
		BadLayout: *badLayout, ColorConflict: *colorConflict || *unknownColor,
		UnknownColor: *unknownColor,
	})
	if err != nil {
		return err
	}
	if *truncate == "cluster" {
		stream, _, err = fixture.TruncateAtCluster(stream, c.frames*3/4)
		if err != nil {
			return err
		}
	}

	cfg := buildConfig(*c)
	cfg.Frames = c.frames
	cfg.RTPLossRate = *loss
	cfg.RTPJitterMs = *jitter
	cfg.RTPHardFraction = *hardFraction

	m, runErr := pipeline.Run(context.Background(), bytes.NewReader(stream), *seed, cfg)
	if runErr != nil {
		return runErr
	}
	printMetrics(m)
	if !m.Published || len(m.Faults) > 0 {
		return timeline.New(timeline.CatUnrecoverable,
			fmt.Sprintf("demo did not publish: %d fault(s)", len(m.Faults)))
	}
	return nil
}

func buildConfig(c commonFlags) pipeline.Config {
	out := c.output
	if out == "" {
		out = filepath.Join(os.TempDir(), "mediacheck-out.mp4")
	}
	return pipeline.Config{
		IndexDir:         c.indexDir,
		OutputPath:       out,
		Frames:           c.frames,
		FPS:              24,
		TargetSampleRate: 48000,
		SyncLimitMs:      20,
		P95LimitMs:       50,
		ColorPolicy:      color.Policy{PreferTrack: true},
		AudioPolicy:      audio.Config{FallbackLayout: "stereo"},
		RTPWindow:        64,
		RTPClockRate:     90000,
	}
}

func openInput(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) && filepath.Dir(clean) == string(filepath.Separator) {
		return nil, timeline.New(timeline.CatConfig, "refusing filesystem-root input")
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, timeline.New(timeline.CatIO, "cannot open input").Wrapf(err)
	}
	return f, nil
}

func printMetrics(m *pipeline.Metrics) {
	view := *m
	// Keep the terminal report compact: show at most a bounded hash list.
	const maxShown = 12
	if len(view.FrameHashes) > maxShown {
		view.FrameHashes = append(append([]string(nil), m.FrameHashes[:maxShown]...),
			fmt.Sprintf("...(%d total)", len(m.FrameHashes)))
	}
	printJSON(view)
}

func printJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "report encode failed: %v\n", err)
	}
}
