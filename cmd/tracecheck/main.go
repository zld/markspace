// Command tracecheck is the offline RTP regression acceptance entry point.
// It is terminal-only: fixtures are generated in-process and no network is used.
package main

import (
	"flag"
	"fmt"
	"os"

	"tracecheck/internal/pipeline"
	"tracecheck/internal/rtptrace"
)

// Exit codes follow the acceptance contract.
const (
	exitOK             = 0
	exitArgs           = 1
	exitUnrecoverable  = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitArgs)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "run":
		os.Exit(runSingle(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(exitArgs)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  tracecheck demo --out <dir>")
	fmt.Fprintln(os.Stderr, "  tracecheck run --input <mp4|mov> --out <dir> [--seed n] [--loss f]")
}

// runSingle processes one explicit input file and maps pipeline outcomes onto
// the documented exit codes: 0 success, 2 unrecoverable/verification failure.
func runSingle(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("input", "", "input MP4/MOV path")
	out := fs.String("out", "", "output directory")
	seed := fs.Uint64("seed", 1, "impairment PRNG seed")
	loss := fs.Float64("loss", 0.2, "packet loss probability")
	if err := fs.Parse(args); err != nil {
		return exitArgs
	}
	if *input == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "run requires --input and --out")
		return exitArgs
	}
	if _, err := os.Stat(*input); err != nil {
		fmt.Fprintf(os.Stderr, "input not readable: %v\n", err)
		return exitArgs
	}
	plan := rtptrace.Plan{
		Seed: *seed, LossRate: *loss,
		DuplicateAt: []int{7}, ReorderAt: []int{11},
	}
	rep, err := pipeline.Run(*input, *out, plan)
	if rep != nil {
		printReport(rep)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return exitUnrecoverable
	}
	return exitOK
}
