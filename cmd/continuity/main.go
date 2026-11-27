// Command continuity runs the local media-continuity pipeline against
// self-generated MP4/MOV fixtures and prints a machine-readable verification
// summary. The library packages under internal/ are the primary deliverable;
// this entry point exists to drive the documented demo scenarios.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/zld/continuity/internal/clock"
)

// Exit codes follow the acceptance contract.
const (
	exitOK             = 0
	exitInternal       = 1
	exitCorrupt        = 2
	exitResumeMismatch = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitInternal
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "-h", "--help", "help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return exitInternal
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `continuity - local media timestamp continuity tool

usage:
  continuity demo [flags]

flags:
  --case name          success (default) | corrupt | resume
  --work dir           working directory for fixtures and checkpoints
  --chunk n            index entries per checkpoint block (default 64)
  --slow-ms n          writer delay per block in milliseconds (default 0)
  --interrupt-after n  stop after n blocks, then resume without the flag
  --samples n          samples to generate in the demo fixture (default 600)
  --sync-every n       mark every nth sample a sync point (default 5)
`)
}

// mapExit selects the documented exit code from a pipeline error.
func mapExit(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, clock.ErrResumeMismatch):
		return exitResumeMismatch
	case errors.Is(err, clock.ErrCorrupt):
		return exitCorrupt
	default:
		return exitInternal
	}
}

// demoFlags centralizes flag parsing so runDemo stays focused on orchestration.
type demoFlags struct {
	caseName       string
	work           string
	chunk          int
	slowMS         int
	interruptAfter int
	samples        int
	syncEvery      int
}

func parseDemoFlags(args []string) (demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var d demoFlags
	fs.StringVar(&d.caseName, "case", "success", "demo case: success|corrupt|resume")
	fs.StringVar(&d.work, "work", "./tmp/demo", "working directory")
	fs.IntVar(&d.chunk, "chunk", 64, "entries per checkpoint block")
	fs.IntVar(&d.slowMS, "slow-ms", 0, "writer delay per block (ms)")
	fs.IntVar(&d.interruptAfter, "interrupt-after", 0, "stop after N blocks")
	fs.IntVar(&d.samples, "samples", 600, "fixture sample count")
	fs.IntVar(&d.syncEvery, "sync-every", 5, "sync sample period")
	if err := fs.Parse(args); err != nil {
		return d, err
	}
	return d, nil
}
