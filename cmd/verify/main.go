package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"mediarepair/internal/acceptance"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scenario := fs.String("scenario", "all", "scenario: all, normal, checksum, truncated-mp4, rtp-wrap, rtp-ambiguous, replace, metadata, interrupt, resume, rollback")
	root := fs.String("root", ".tmp/acceptance", "working directory (must stay inside this path)")
	frames := fs.Int("checkpoint-frames", 4, "frames per checkpoint block")
	afterBlock := fs.Int("after-block", 1, "interrupt after this many committed blocks")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return acceptance.ExitOK
		}
		fmt.Fprintln(stderr, "argument_error=", err.Error())
		return acceptance.ExitUsage
	}
	if *frames <= 0 {
		fmt.Fprintln(stdout, "argument_error=checkpoint-frames must be positive")
		return acceptance.ExitUsage
	}
	return acceptance.Run(acceptance.Options{
		Scenario:         *scenario,
		Root:             *root,
		CheckpointFrames: *frames,
		AfterBlock:       *afterBlock,
	}, stdout)
}
