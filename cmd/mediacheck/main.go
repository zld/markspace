// Command mediacheck runs the local Matroska/WebM, track, fragment and RTP
// validation kernel over self-generated terminal demos.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"mediacheck/internal/pipeline"
	"mediacheck/internal/sample"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(stderr, "usage: mediacheck demo [--seed N] [--case NAME]")
		fmt.Fprintln(stderr, "cases: normal, truncated-cluster, audio-conflict, rtp-gap")
		return 2
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 7, "deterministic sample seed")
	caseName := fs.String("case", sample.CaseNormal, "sample case name")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || !sample.ValidCase(*caseName) {
		fmt.Fprintf(stderr, "invalid --case %q\n", *caseName)
		return 2
	}

	bundle, err := sample.Generate(*caseName, *seed)
	if err != nil {
		fmt.Fprintf(stderr, "generate sample: %v\n", err)
		return 2
	}

	rep := pipeline.ValidateBundle(bundle)
	sampleName := fmt.Sprintf("demo-%s-seed-%d", bundle.Case, bundle.Seed)
	rep.Finalize(sampleName, bundle.Case, bundle.Digest, bundle.Seed)
	if err := rep.WriteTerminal(stdout); err != nil {
		fmt.Fprintf(stderr, "write report: %v\n", err)
		return 1
	}
	return rep.Summary.ExitCode
}
