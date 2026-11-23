// Command mediadist is the acceptance entry point for the media distribution
// kernel. It generates self-contained MP4 fixtures, runs the pipeline into
// sharded manifests, verifies published output and exercises recovery.
package main

import (
	"fmt"
	"os"
)

// Exit codes follow the acceptance contract.
const (
	exitOK        = 0
	exitInternal  = 1
	exitUnrecover = 2
	exitUsage     = 3
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "recover":
		err = runRecover(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
	if err == nil {
		os.Exit(exitOK)
	}
	if code := errorCode(err); code != "BAD_ARGUMENT" {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
	}
	os.Exit(classifyExit(err))
}

func usage() {
	fmt.Fprint(os.Stderr, `mediadist — edge media distribution kernel

usage:
  mediadist demo --seed N --duration 12s --bitrates 800k,1600k,3000k --out DIR
                 [--case truncated-index|timestamp-backward|bad-layout|
                        color-conflict|missing-keyframe]
                 [--color-priority container|bitstream|cli]
  mediadist verify  --input DIR
  mediadist recover --input DIR --out DIR

exit codes: 0 success | 1 internal | 2 unrecoverable input | 3 bad arguments
`)
}

// classifyExit maps a classified pipeline error onto the documented exit code.
func classifyExit(err error) int {
	code := errorCode(err)
	switch code {
	case "":
		return exitOK
	case "BAD_ARGUMENT":
		return exitUsage
	// Input faults the distribution kernel cannot publish as-is. Recoverable
	// ones (INDEX_TRUNCATED, TIMESTAMP_BACKWARD) still fail a plain demo run;
	// only the explicit `recover` command turns them into exit 0.
	case "KEYFRAME_MISSING", "COLOR_CONFLICT", "COLOR_PENDING",
		"LAYOUT_UNSUPPORTED", "ALIGNMENT_DRIFT", "DURATION_NOT_CONSERVED",
		"INDEX_TRUNCATED", "TIMESTAMP_BACKWARD", "GRAPH_EMPTY",
		"INPUT_LIMIT_EXCEEDED", "CONTAINER_INVALID":
		return exitUnrecover
	default:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitInternal
	}
}
