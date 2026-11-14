// Command transcode builds segmented, bitrate-capped H.264 encodes and
// produces per-segment PSNR quality evidence. All processing is local: inputs,
// FFmpeg/ffprobe from PATH, no network access.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"transcode/internal/config"
	"transcode/internal/media"
	"transcode/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:])
	case "run":
		return cmdRun(args[1:])
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  transcode demo [--out DIR] [--segment 2s] [--target-kbps 900] [flags]
  transcode run  --input FILE [--input FILE ...] [flags]

flags: --out --segment --target-kbps --maxrate-kbps --concurrency
       --retries --timeout --fps --preset --resume`)
}

func newFlagSet(name string, cfg *config.Config, resume *bool) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&cfg.OutDir, "out", cfg.OutDir, "output directory for all artifacts")
	fs.DurationVar(&cfg.Segment, "segment", cfg.Segment, "segment length (e.g. 2s)")
	fs.IntVar(&cfg.TargetKbps, "target-kbps", cfg.TargetKbps, "target average video bitrate")
	fs.IntVar(&cfg.MaxrateKbps, "maxrate-kbps", 0, "peak bitrate constraint (default 2x target)")
	fs.IntVar(&cfg.Concurrency, "concurrency", cfg.Concurrency, "max concurrent FFmpeg processes")
	fs.IntVar(&cfg.Retries, "retries", cfg.Retries, "retries per job after a failed attempt")
	fs.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "per-process timeout")
	fs.IntVar(&cfg.FPS, "fps", cfg.FPS, "constant output frame rate")
	fs.StringVar(&cfg.Preset, "preset", cfg.Preset, "x264 preset")
	fs.BoolVar(resume, "resume", false, "resume an interrupted run from the state log")
	return fs
}

// execute runs the pipeline with signal handling and maps the result to an
// exit code.
func execute(cfg config.Config, specs []pipeline.JobSpec, resume bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := pipeline.Run(ctx, pipeline.Options{
		Cfg: cfg, Jobs: specs, Resume: resume, Stdout: os.Stdout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if rep.JobsFailed > 0 || rep.JobsCanceled > 0 {
		return 1
	}
	return 0
}

// cmdDemo generates the three built-in fixtures (static, fast motion, short
// final segment) and processes them end to end.
func cmdDemo(args []string) int {
	cfg := config.Default()
	var resume bool
	fs := newFlagSet("demo", &cfg, &resume)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	fixDir := filepath.Join(cfg.OutDir, "fixtures")
	if err := os.MkdirAll(fixDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := media.Runner{Timeout: cfg.Timeout}
	var specs []pipeline.JobSpec
	for _, kind := range media.FixtureKinds {
		p := filepath.Join(fixDir, kind+".mp4")
		if _, err := os.Stat(p); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "generating fixture %s\n", p)
			if err := runner.GenerateFixture(ctx, kind, p, cfg.FPS); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
		}
		specs = append(specs, pipeline.JobSpec{ID: kind, Input: p})
	}
	return execute(cfg, specs, resume)
}

type strList []string

func (s *strList) String() string { return strings.Join(*s, ",") }
func (s *strList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// cmdRun processes user-supplied local input files.
func cmdRun(args []string) int {
	cfg := config.Default()
	var resume bool
	var inputs strList
	fs := newFlagSet("run", &cfg, &resume)
	fs.Var(&inputs, "input", "input file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	seen := map[string]int{}
	var specs []pipeline.JobSpec
	for _, in := range inputs {
		base := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
		id := base
		if n := seen[base]; n > 0 {
			id = fmt.Sprintf("%s_%d", base, n)
		}
		seen[base]++
		specs = append(specs, pipeline.JobSpec{ID: id, Input: in})
	}
	return execute(cfg, specs, resume)
}
