// Command rtptrace runs the deterministic weak-network RTP regression kernel.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"rtptrace/internal/manifest"
	"rtptrace/internal/pipeline"
)

const usage = `rtptrace demo [flags]

Commands:
  demo    generate seeded fixtures and run the full alignment/evidence pipeline

Flags:
  --seed int          deterministic fixture seed (default 17)
  --duration dur      source duration (default 120s)
  --segment dur       segment duration (default 6s)
  --bitrates list     comma-separated ladder, e.g. 800k,1600k
  --fault string      none|truncated-stbl|edit-out-of-range|invalid-dependency|missing-keyframe
  --recover           apply deterministic recovery where supported
  --report string     output format: json

Exit codes:
  0 success; 2 corrupt input with a usable JSON recovery report; other non-zero usage/internal error.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		return 0
	}
	if args[0] != "demo" {
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 64
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "deterministic seed")
	durationText := fs.String("duration", "120s", "total duration")
	segmentText := fs.String("segment", "6s", "segment duration")
	bitrateText := fs.String("bitrates", "800k,1600k", "comma-separated target bitrates")
	fault := fs.String("fault", "", "deterministic fault")
	recoverFlag := fs.Bool("recover", false, "recover supported faults")
	report := fs.String("report", "json", "report format")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 64
	}
	if *report != "json" {
		fmt.Fprintf(stderr, "unsupported report format %q; only json is generated\n", *report)
		return 64
	}

	duration, err := time.ParseDuration(*durationText)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --duration: %v\n", err)
		return 64
	}
	segment, err := time.ParseDuration(*segmentText)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --segment: %v\n", err)
		return 64
	}
	bitrates, err := manifest.CanonicalBitrates(strings.Split(*bitrateText, ","))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 64
	}
	faultName := *fault
	if faultName == "none" {
		faultName = ""
	}
	if !pipeline.AllowedFaults()[faultName] {
		fmt.Fprintf(stderr, "unsupported --fault %q\n", faultName)
		return 64
	}

	outcome, err := pipeline.Run(pipeline.Options{
		Seed: *seed, Duration: duration, SegmentDuration: segment,
		Bitrates: bitrates, Fault: faultName, Recover: *recoverFlag,
	})
	if err != nil {
		fmt.Fprintf(stderr, "internal error: %v\n", err)
		return 1
	}
	data, err := outcome.Report.JSON()
	if err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(append(data, '\n')); err != nil {
		return 1
	}
	return outcome.ExitCode
}
