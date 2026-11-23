// Command mediaroute is the terminal acceptance adapter over the reusable
// media-processing kernel. It has two subcommands:
//
//	mediaroute demo   [--seed N] [--loss-burst N] [--reorder-window N]
//	                  [--crash-at index-commit|segment-commit|wal-begin]
//	                  [--corrupt-index|--corrupt-stream] [--journal-dir DIR]
//	mediaroute verify --seed N --repeat K [--journal-dir DIR]
//
// demo runs one pass; with --crash-at it instead crashes at the named journal
// boundary, recovers the committed prefix, and reruns, so removed work is
// visible in the recovery_* lines and the rerun hash proves consistency.
//
// Exit codes: 0 success, 2 input/index corruption, 3 recovery/consistency
// failure, 4 bad arguments.
package main

import (
	"fmt"
	"os"
	"strings"

	"mediaroute/internal/clock"
	"mediaroute/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(pipeline.ExitBadArguments)
	}
	switch os.Args[1] {
	case "demo":
		code := runDemo(os.Args[2:])
		os.Exit(code)
	case "verify":
		code := runVerify(os.Args[2:])
		os.Exit(code)
	case "-h", "--help", "help":
		usage()
		os.Exit(pipeline.ExitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(pipeline.ExitBadArguments)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, strings.TrimSpace(`
mediaroute - edge media review-timeline kernel

usage:
  mediaroute demo   --seed N [--loss-burst N] [--reorder-window N]
                    [--crash-at index-commit|segment-commit|wal-begin]
                    [--corrupt-index] [--corrupt-stream] [--journal-dir DIR]
  mediaroute verify --seed N --repeat K [--journal-dir DIR]

demo with --crash-at injects a crash at that journal boundary, recovers the
committed prefix, then reruns to completion.

exit codes: 0 ok | 2 input/index corruption | 3 recovery/consistency | 4 args
`)+"\n")
}

// failWith prints the failure stage with pipeline error context and returns
// the mapped exit code.
func failWith(err error) int {
	pe, ok := clock.AsError(err)
	if !ok {
		fmt.Printf("status=failed phase=unknown recoverable=false error=%q\n", err.Error())
		return pipeline.ExitRecoveryFailure
	}
	code := pipeline.ExitRecoveryFailure
	if !pe.Recoverable && (pe.Phase == clock.PhaseIndex || pe.Phase == clock.PhaseCodec) {
		code = pipeline.ExitCorruptInput
	}
	fmt.Printf("status=failed phase=%s track=%d seq=%d recoverable=%t error=%q\n",
		pe.Phase, pe.Track, pe.Seq, pe.Recoverable, pe.Message)
	return code
}
