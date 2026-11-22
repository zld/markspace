// Command medpipe generates deterministic multi-track fixtures and runs the
// full media pipeline, printing a verifiable terminal summary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"medpipe/app"
	"medpipe/stream"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		seed     = fs.Int64("seed", 17, "fixture PRNG seed")
		rate     = fs.Uint("rate", 48000, "output sample rate in Hz")
		channels = fs.Int("channels", 6, "fixture channel count (1/2/6/8)")
		queue    = fs.Int("queue", 4, "bounded queue capacity (hard ceiling 64)")
		slowMS   = fs.Int("slow-ms", 0, "per-block consumer delay in milliseconds")
		outDir   = fs.String("out", "/tmp/medpipe-demo", "output directory")
		fixtureName = fs.String("fixture", "", "fault fixture: truncated|missing|misaligned|seqjump|badlength")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *queue > stream.MaxQueueBlocks || *queue <= 0 {
		fmt.Fprintf(os.Stderr, "queue capacity must be in [1,%d]\n", stream.MaxQueueBlocks)
		return 1
	}
	opt := app.Options{
		Seed: *seed, Rate: uint32(*rate), Channels: *channels, Queue: *queue,
		SlowMS: *slowMS, OutDir: *outDir, Fixture: *fixtureName,
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "output directory: %v\n", err)
		return 1
	}
	sum, err := app.Run(context.Background(), opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: %v\n", err)
		return 1
	}
	app.PrintSummary(sum)
	return sum.ExitCode
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: medpipe demo [flags]")
	fmt.Fprintln(os.Stderr, "flags:")
	fmt.Fprintln(os.Stderr, "  --seed int        fixture PRNG seed")
	fmt.Fprintln(os.Stderr, "  --rate uint       output sample rate in Hz")
	fmt.Fprintln(os.Stderr, "  --channels int    fixture channel count (1/2/6/8)")
	fmt.Fprintln(os.Stderr, "  --queue int       bounded queue capacity (hard ceiling 64)")
	fmt.Fprintln(os.Stderr, "  --slow-ms int     per-block consumer delay in milliseconds")
	fmt.Fprintln(os.Stderr, "  --out string      output directory")
	fmt.Fprintln(os.Stderr, "  --fixture string  fault: truncated|missing|misaligned|seqjump|badlength")
}
