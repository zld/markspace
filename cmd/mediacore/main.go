// Command mediacore is the thin terminal entry point for demo and acceptance workflows.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mediacore/internal/app"
	"mediacore/internal/diagnostic"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(diagnostic.ExitGeneric)
	}
	command := os.Args[1]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	work := fs.String("work", "./tmp/demo", "local working directory (all paths stay below it)")
	memoryMB := fs.Int("memory-mb", 64, "in-memory buffer cap in MiB")
	caseName := fs.String("case", "", "optional acceptance case to run directly")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(diagnostic.ExitGeneric)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch command {
	case "demo":
		err = app.Demo(ctx, *work, *memoryMB)
	case "verify":
		err = app.Verify(ctx, *work, *memoryMB, *caseName)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		usage()
		os.Exit(diagnostic.ExitGeneric)
	}
	if err != nil {
		pos := diagnostic.PositionOf(err)
		fmt.Fprintf(os.Stderr, "ERROR exit=%d: %s\n", diagnostic.ExitCode(err), pos.Message)
		if pos.Kind != "" || pos.EntryIndex != 0 || pos.ByteOffset != 0 {
			fmt.Fprintf(os.Stderr, "LOCATION stage=%s kind=%s manifest=%s rep=%s entry=%d seq=%d byte=%d\n",
				pos.Stage, pos.Kind, pos.ManifestPath, pos.Representation, pos.EntryIndex, pos.Sequence, pos.ByteOffset)
		}
		os.Exit(diagnostic.ExitCode(err))
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediacore <demo|verify> --work <dir> [--memory-mb N] [--case name]")
}
