// Command pwflow correlates client events, RTP records and Matroska/WebM
// media on one integer nanosecond timeline.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

// Exit codes are part of the local acceptance contract.
const (
	exitOK          = 0
	exitGaps        = 2
	exitRejected    = 3
	exitInterrupted = 75
)

type cliError struct {
	code int
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(runCLI(ctx, os.Args[1:]))
}

func runCLI(ctx context.Context, args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitRejected
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "fixtures":
		err = runFixtures(ctx, rest)
	case "run":
		err = runPipelineCommand(ctx, rest)
	case "verify":
		err = runVerifyCommand(ctx, rest)
	case "-h", "--help", "help":
		usage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage(os.Stderr)
		return exitRejected
	}
	if err == nil {
		return exitOK
	}
	var ce *cliError
	if errors.As(err, &ce) {
		if ce.code == exitInterrupted {
			fmt.Fprintf(os.Stdout, "CHECKPOINT: %v\n", ce.err)
		} else {
			fmt.Fprintf(os.Stderr, "ERROR(%d): %v\n", ce.code, ce.err)
		}
		return ce.code
	}
	fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
	return exitRejected
}

func usage(w *os.File) {
	fmt.Fprintln(w, `pwflow — local playback-failure timeline tool

Usage:
  pwflow fixtures --dir DIR [--seed N]
  pwflow run --input MEDIA --events EVENTS --rtp TRACE --out DIR [options]
  pwflow verify --manifest MANIFEST

Run options:
  --budget-mib N       memory budget in MiB (default 64)
  --workers N          worker count (default 4)
  --checkpoint PATH    resumable checkpoint file
  --stop-after N       exit 75 after N completed segments

Exit codes:
  0 complete PASS; 2 produced/verified with recoverable gaps;
  3 invalid input or budget; 75 resumable checkpoint interruption.`)
}
