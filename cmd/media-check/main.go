// Command media-check validates and publishes multi-codec media
// resources from the terminal. All interaction is via flags and
// terminal output; there is no graphical interface.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"mediacheck/internal/codec"
	"mediacheck/internal/pipeline"
)

func main() { os.Exit(run(os.Args)) }

func run(args []string) int {
	if len(args) < 2 {
		usage()
		return 2
	}
	switch args[1] {
	case "demo":
		return demo(args[2:])
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: media-check demo --workdir DIR [flags]

flags:
  --codecs h264,h265,av1   codecs to process
  --corrupt-offset N       corruption demo at byte N (-1 disables)
  --interrupt-after STAGE  stop after migration stage (e.g. migrate-1)
  --recover                recover the release store after interruption
  --budget-bytes N         processing budget in bytes
  --max-file-bytes N       per-stream size limit
  --max-tracks N           track count limit
  --timeout D              processing deadline (e.g. 30s)

exit codes: 0 success, 2 input error, 3 processing interruption`)
}

func demo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	workdir := fs.String("workdir", "/tmp/media-check", "working directory")
	codecsFlag := fs.String("codecs", "h264,h265,av1", "comma-separated codecs")
	corrupt := fs.Int64("corrupt-offset", -1, "corruption demo offset")
	interrupt := fs.String("interrupt-after", "", "stop after this migrate stage")
	recover := fs.Bool("recover", false, "recover release store")
	budget := fs.Int64("budget-bytes", 0, "processing budget")
	maxFile := fs.Int64("max-file-bytes", 0, "per-stream limit")
	maxTracks := fs.Int("max-tracks", 0, "track limit")
	timeout := fs.Duration("timeout", 0, "processing deadline")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var kinds []codec.Kind
	for _, name := range strings.Split(*codecsFlag, ",") {
		k, err := codec.ParseKind(strings.TrimSpace(name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		kinds = append(kinds, k)
	}
	return pipeline.Run(pipeline.Options{
		Workdir: *workdir, Codecs: kinds,
		CorruptOffset: *corrupt, InterruptAfter: *interrupt, Recover: *recover,
		BudgetBytes: *budget, MaxFileBytes: *maxFile, MaxTracks: *maxTracks,
		Timeout: *timeout,
	}, os.Stdout)
}
