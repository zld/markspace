// Command assetcheck is the terminal acceptance adapter for the media
// processing kernel. It analyzes WAV/ABLK assets, evaluates business
// thresholds, maintains fingerprint caches and commits integer-frame indexes
// through a write-ahead transaction log.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"assetcheck/internal/fixture"
	"assetcheck/internal/stream"
)

// DefaultMaxFileMB is the in-memory read budget for one input file.
const DefaultMaxFileMB = 256

// Exit codes mandated by the acceptance contract.
const (
	exitOK     = 0
	exitBudget = 2
	exitFormat = 3
	exitTxn    = 4
	exitUsage  = 1
)

type options struct {
	generate      string
	input         string
	indexDir      string
	report        string
	mutate        string
	injectFailure string
	resume        bool
	maxScanFrames int64
	maxFileMB     int
	thresholds    thresholdFlag
}

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run parses args and executes one command, returning the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	opts := options{thresholds: defaultThresholdFlag()}
	fs := flag.NewFlagSet("assetcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.generate, "generate", "", "generate self-contained fixtures into `dir`")
	fs.StringVar(&opts.input, "input", "", "WAV file to analyze")
	fs.StringVar(&opts.indexDir, "index", "./.assetcheck-index", "index/cache/WAL `dir`")
	fs.StringVar(&opts.report, "report", "text", "report format: text|json")
	fs.StringVar(&opts.mutate, "mutate", "", "apply one mutation before analysis: content|container|metadata")
	fs.StringVar(&opts.injectFailure, "inject-failure", "", "inject failure at phase, e.g. after-index-prepare")
	fs.BoolVar(&opts.resume, "resume", false, "recover interrupted transactions and run to completion")
	fs.Int64Var(&opts.maxScanFrames, "max-scan-frames", stream.DefaultMaxScanFrames,
		"scan budget in sample frames before reporting the last processed frame")
	fs.IntVar(&opts.maxFileMB, "max-file-mb", DefaultMaxFileMB,
		"memory budget for one fully-read input file, in MiB")
	fs.Var(&opts.thresholds, "threshold", "comma-separated key=value thresholds (repeatable)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return exitOK
		}
		return exitUsage
	}

	if opts.generate != "" {
		paths, err := fixture.Generate(opts.generate)
		if err != nil {
			fmt.Fprintf(stderr, "generate failed: %v\n", err)
			return exitUsage
		}
		if opts.report == "json" {
			writeJSON(stdout, map[string]any{"generated": paths})
		} else {
			fmt.Fprintf(stdout, "generated %d fixture(s) in %s:\n", len(paths), opts.generate)
			for _, p := range paths {
				fmt.Fprintln(stdout, "  "+p)
			}
		}
		return exitOK
	}

	if opts.input == "" {
		fmt.Fprintln(stderr, "assetcheck: -input is required (or use -generate)")
		fs.Usage()
		return exitUsage
	}
	if opts.report != "text" && opts.report != "json" {
		fmt.Fprintf(stderr, "assetcheck: unsupported -report %q\n", opts.report)
		return exitUsage
	}
	if opts.mutate != "" && opts.mutate != "content" && opts.mutate != "container" && opts.mutate != "metadata" {
		fmt.Fprintf(stderr, "assetcheck: -mutate must be content|container|metadata\n")
		return exitUsage
	}

	view, err := runAnalyze(opts)
	if view != nil {
		if opts.report == "json" {
			_ = writeJSON(stdout, view)
		} else {
			fmt.Fprint(stdout, RenderText(view))
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitCodeFor(err)
	}
	if view != nil && !view.EvalPass {
		return exitBudget
	}
	return exitOK
}
