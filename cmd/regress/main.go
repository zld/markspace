// Command regress is the terminal-only acceptance entry point. It generates
// fixtures, runs the scan/plan/publish/verify pipeline and prints the
// explainable metrics; all reusable logic lives in internal packages.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"regress/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "demo":
		runDemo(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: regress demo [flags]

flags:
  --codec h264|h265|av1   elementary stream codec (default h264)
  --segments N            number of keyframe-aligned segments (default 4)
  --ssim-min F            minimum acceptable SSIM (default 0.93)
  --max-bitrate-bps N     peak bitrate ceiling (default 600000)
  --work-budget N         deterministic work budget (default 200000)
  --job KEY               stable job key for recovery (default demo-job)
  --fixture NAME          damage fixture: corrupt-index (default clean)
  --resume                claim existing products instead of republishing
  --inject NAME           fault injection: crash-after-publish
  --root DIR              working root directory (default ./work)
`)
}

type demoFlags struct {
	codec      string
	segments   int
	ssimMin    float64
	maxBitrate int64
	workBudget int64
	job        string
	fixture    string
	resume     bool
	inject     string
	root       string
}

func parseDemo(args []string) (*demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	d := &demoFlags{}
	fs.StringVar(&d.codec, "codec", "h264", "codec")
	fs.IntVar(&d.segments, "segments", 4, "segment count")
	fs.Float64Var(&d.ssimMin, "ssim-min", 0.93, "minimum SSIM")
	fs.Int64Var(&d.maxBitrate, "max-bitrate-bps", 600000, "peak bitrate ceiling")
	fs.Int64Var(&d.workBudget, "work-budget", 200000, "work budget")
	fs.StringVar(&d.job, "job", "demo-job", "job key")
	fs.StringVar(&d.fixture, "fixture", "", "damage fixture")
	fs.BoolVar(&d.resume, "resume", false, "resume an existing job")
	fs.StringVar(&d.inject, "inject", "", "fault injection")
	fs.StringVar(&d.root, "root", "", "working root")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	switch d.codec {
	case "h264", "h265", "av1":
	default:
		return nil, fmt.Errorf("unsupported codec %q", d.codec)
	}
	return d, nil
}

// runDemo terminates the process with the scenario-specific exit code, which
// lets `go run` forward the real status instead of masking it as a generic 1.
func runDemo(args []string) {
	d, err := parseDemo(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root := d.root
	if root == "" {
		root = filepath.Join("work", d.job)
	}
	opt := pipeline.Options{
		JobKey: d.job, Codec: d.codec, Segments: d.segments,
		SSIMMin: d.ssimMin, MaxBitrateBPS: d.maxBitrate, WorkBudget: d.workBudget,
		RootDir: root, Resume: d.resume, Concurrency: 2,
	}
	if d.inject == "crash-after-publish" {
		opt.Inject = pipeline.InjectCrashAfterPublish
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if d.fixture == "corrupt-index" {
		os.Exit(runCorruptIndex(ctx, opt))
	}

	m, runErr := pipeline.Run(ctx, opt)
	printMetrics(m)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		if m != nil && m.ExitCode != 0 {
			os.Exit(m.ExitCode)
		}
		os.Exit(1)
	}
	os.Exit(m.ExitCode)
}
