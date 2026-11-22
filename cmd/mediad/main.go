// Command mediad turns timestamped camera media into an editor hand-off list:
// it segments on sync frames, detects silence, fingerprints slices, survives
// interruption, and verifies every recorded hash.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/pairwise/mediad/internal/fault"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad - local media pipeline

usage:
  mediad demo   --out DIR [--fresh]
  mediad verify --dir DIR

exit codes: 0 all invariants passed, 1 damaged input with recoverable output,
            2 index or verification failure
`)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mediad: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "directory that receives generated media and results")
	fresh := fs.Bool("fresh", false, "remove an existing output directory first")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "mediad demo: --out is required")
		return 2
	}
	summary, err := demo(*out, *fresh)
	printSummary(summary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad demo: %v\n", err)
		return fault.ExitCode(err)
	}
	return 0
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory produced by mediad demo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "mediad verify: --dir is required")
		return 2
	}
	summary, err := verifyDir(*dir)
	printSummary(summary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad verify: %v\n", err)
		return fault.ExitCode(err)
	}
	return 0
}
