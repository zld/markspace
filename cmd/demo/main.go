// Command demo is the acceptance adapter for the replay slicing library.
// It generates sample fixtures and runs the slice/verify pipeline.
// Exit codes: 0 = constraints passed, 1 = input/output constraint
// failure, 2 = parameter error.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

type options struct {
	generate bool
	out      string
	input    string
	event    string
	before   time.Duration
	after    time.Duration
	bitrate  int64
	workers  int
	verify   bool
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var o options
	fs.BoolVar(&o.generate, "generate", false, "generate sample fixture and exit")
	fs.StringVar(&o.out, "out", "", "output directory for --generate")
	fs.StringVar(&o.input, "input", "", "fixture directory to process")
	fs.StringVar(&o.event, "event", "", "event id to slice")
	fs.DurationVar(&o.before, "window-before", 3*time.Second, "window before the event")
	fs.DurationVar(&o.after, "window-after", 5*time.Second, "window after the event")
	fs.Int64Var(&o.bitrate, "target-bitrate", 1_800_000, "target output bitrate (bit/s)")
	fs.IntVar(&o.workers, "workers", 2, "concurrent workers")
	fs.BoolVar(&o.verify, "verify", false, "verify produced clips and report metrics")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch {
	case o.generate:
		if o.out == "" {
			fmt.Fprintln(os.Stderr, "demo: --out is required with --generate")
			return 2
		}
		if err := generateFixture(o.out); err != nil {
			fmt.Fprintln(os.Stderr, "demo: generate:", err)
			return 1
		}
		fmt.Println("fixture written to", o.out)
		return 0
	case o.input != "":
		if o.event == "" {
			fmt.Fprintln(os.Stderr, "demo: --event is required with --input")
			return 2
		}
		if o.before < 0 || o.after < 0 || o.bitrate <= 0 || o.workers < 1 {
			fmt.Fprintln(os.Stderr, "demo: invalid window/bitrate/workers parameter")
			return 2
		}
		return runPipeline(o)
	default:
		fs.Usage()
		return 2
	}
}
