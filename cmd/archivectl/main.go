// archivectl is a terminal-only recovery controller for damaged Matroska/WebM
// archives. It generates synthetic fixtures, runs the deterministic recovery
// pipeline and emits frame hashes plus a structured metrics summary.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"archivectl/internal/fixture"
	"archivectl/internal/pipeline"
)

// Exit codes per the accepted contract.
const (
	exitOK             = 0 // processed, including classifiable local damage
	exitUsage          = 2 // invalid parameters
	exitUnrecoverable  = 3 // input cannot be recovered at all
	exitInvariant      = 4 // cross-stage invariant failed
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, strings.TrimSpace(`
usage: archivectl demo --seed <int> --loss-window <1..256> --out <dir>

Generates normal, truncated, corrupt and RTP-loss fixtures, runs the recovery
pipeline, writes PNG frames and a SHA-256 manifest under --out, and prints a
deterministic metrics summary to stdout.
`)+"\n")
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "deterministic RNG seed")
	lossWindow := fs.Int("loss-window", 8, "RTP loss window (1..256)")
	out := fs.String("out", "/tmp/archive-demo", "output directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *lossWindow <= 0 || *lossWindow > pipeline.MaxLossWindow {
		fmt.Fprintf(os.Stderr, "error: --loss-window must be in 1..%d\n",
			pipeline.MaxLossWindow)
		return exitUsage
	}
	if strings.TrimSpace(*out) == "" {
		fmt.Fprintln(os.Stderr, "error: --out must not be empty")
		return exitUsage
	}

	set := fixture.Generate(*seed, *lossWindow)
	opts := pipeline.DefaultOptions()
	opts.Seed = *seed
	opts.LossWindow = *lossWindow

	res, err := pipeline.Run(set, opts)
	if err != nil {
		// A configuration/usage problem is distinct from data failure.
		if se, ok := err.(pipeline.StageError); ok &&
			(se.Stage == "config" || se.Stage == "rtp" && se.Category == "window_config") {
			fmt.Fprintln(os.Stderr, "parameter error:", err)
			return exitUsage
		}
		fmt.Fprintln(os.Stderr, "unrecoverable input:", err)
		return exitUnrecoverable
	}

	// Cross-stage invariants gate the output: a violation means the pipeline
	// cannot be trusted even though parsing partially succeeded.
	if code := checkInvariants(res); code != exitOK {
		return code
	}

	if err := attachAndWrite(res, set, *seed, *lossWindow, *out); err != nil {
		fmt.Fprintln(os.Stderr, "write error:", err)
		return exitUnrecoverable
	}

	printSummary(res, set)
	return exitOK
}

// checkInvariants validates monotonicity, track references and frame range.
func checkInvariants(res *pipeline.Result) int {
	last := int64(0)
	for i, s := range res.Merged.Samples {
		if !res.Graph.Has(s.TrackNumber) {
			fmt.Fprintf(os.Stderr, "invariant: invalid track reference %d\n",
				s.TrackNumber)
			return exitInvariant
		}
		if i > 0 && s.StartNs < last {
			fmt.Fprintln(os.Stderr, "invariant: non-monotonic timeline")
			return exitInvariant
		}
		last = s.StartNs
	}
	for _, f := range res.Frames {
		if !res.Merged.Clock.InRange(f.TimeNs) {
			fmt.Fprintf(os.Stderr, "invariant: frame %d outside source range\n",
				f.TimeNs)
			return exitInvariant
		}
	}
	return exitOK
}
