// Command mediapipeline is the terminal acceptance driver for the reusable
// media processing library.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"mediapipeline/internal/demo"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediapipeline demo --work DIR [--workers N] [--queue N] [--chunk-bytes N] [--crash-after-publish N]")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "", "persistent work directory")
	workers := fs.Int("workers", 2, "bounded worker count")
	queue := fs.Int("queue", 2, "bounded queue capacity")
	chunkBytes := fs.Int("chunk-bytes", 1<<20, "maximum bytes per sample chunk")
	crashAfterPublish := fs.Int("crash-after-publish", 0, "exit with code 86 after N publishes")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(1)
	}

	err := demo.Run(demo.Config{
		Work:              *work,
		Workers:           *workers,
		Queue:             *queue,
		ChunkBytes:        *chunkBytes,
		CrashAfterPublish: *crashAfterPublish,
		Stdout:            os.Stdout,
	})
	switch {
	case err == nil:
		os.Exit(0)
	case errors.Is(err, demo.ErrInjectedCrash):
		os.Exit(86)
	case errors.Is(err, demo.ErrCorruptInputs):
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
