// Command regress is the terminal entry point of the media regression kernel.
//
// Usage:
//
//	regress demo --seed 17 --duration 600 --versions 3 --window 64
//
// The demo self-generates every fixture: healthy multi-version media,
// reordered/duplicate/lossy RTP streams, truncated boxes, a bad index and a
// slow consumer. It prints quantised metrics and exits 0 on success, 2 when
// input is damaged or a conflict is unresolved, and 1 on an internal error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mediapipe/internal/demo"
	"mediapipe/internal/model"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "regress: unknown command %q\n", args[0])
		usage(stderr)
		return 1
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `regress - local media regression kernel

usage:
  regress demo [flags]

flags:
  --seed N        fixture seed (default 17)
  --duration S    seconds of generated media (default 600)
  --versions N    renditions per asset, >= 2 (default 3)
  --window N      RTP reorder window in packets, >= 8 (default 64)
  --buffer N      pipeline channel depth, >= 1 (default 4)
  --memory MB     scanner memory budget in MiB (default 32)
  --slow MS       slow-consumer delay per item in milliseconds (default 1)

exit codes:
  0  all checks passed and results were committed
  2  damaged input or unresolved conflict (results rolled back)
  1  internal error
`)
}

func runDemo(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o demo.Options
	fs.Int64Var(&o.Seed, "seed", 17, "fixture seed")
	fs.IntVar(&o.Duration, "duration", 600, "seconds of generated media")
	fs.IntVar(&o.Versions, "versions", 3, "renditions per asset")
	fs.IntVar(&o.Window, "window", 64, "RTP reorder window in packets")
	fs.IntVar(&o.Buffer, "buffer", 4, "pipeline channel depth")
	fs.IntVar(&o.MemoryMB, "memory", 32, "scanner memory budget in MiB")
	fs.IntVar(&o.SlowMS, "slow", 1, "slow consumer delay per item in ms")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "regress: unexpected arguments: %v\n", fs.Args())
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out, err := demo.Run(ctx, o)
	if out != nil {
		for _, line := range out.Lines {
			fmt.Fprintln(stdout, line)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "regress: %v\n", err)
		return model.ExitCodeOf(err)
	}
	if out == nil {
		fmt.Fprintln(stderr, "regress: demo produced no outcome")
		return 1
	}
	return out.ExitCode
}
