// Command medtrace reconstructs and reports on a local media interval.
//
// Usage:
//
//	medtrace verify --work DIR --seed N
//	medtrace trace  --root DIR --manifest FILE --from DUR --to DUR [--events FILE]
//
// Exit codes: 0 report completed (recorded anomalies included), 2 invalid
// arguments or manifest, 3 local I/O or report commit failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"medtrace/internal/core"
	"medtrace/internal/events"
	"medtrace/internal/index"
	"medtrace/internal/manifest"
	"medtrace/internal/report"
	"medtrace/internal/stream"
	"medtrace/internal/verify"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "verify":
		return cmdVerify(args[1:], stdout, stderr)
	case "trace":
		return cmdTrace(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "medtrace: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `medtrace - reconstruct and trace a local media interval

usage:
  medtrace verify --work DIR --seed N
  medtrace trace  --root DIR --manifest FILE --from DUR --to DUR [--events FILE]

exit codes:
  0 report completed (recorded anomalies included)
  2 invalid arguments or manifest
  3 local I/O or report commit failure
`)
}

// cmdVerify runs the self-contained acceptance scenario.
func cmdVerify(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	work := fs.String("work", "", "work directory (created if absent)")
	seed := fs.Int64("seed", 0, "deterministic generation seed")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *work == "" {
		fmt.Fprintln(stderr, "medtrace verify: --work is required")
		return 2
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		fmt.Fprintf(stderr, "medtrace verify: %v\n", err)
		return 3
	}
	return verify.Run(verify.Options{Work: *work, Seed: *seed, Out: stdout})
}

// cmdTrace reconstructs a named interval from a manifest and prints a report.
func cmdTrace(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "media root directory")
	manifestPath := fs.String("manifest", "", "root-relative manifest (MPD or m3u8)")
	fromStr := fs.String("from", "", "interval start (e.g. 0s, 1.5s)")
	toStr := fs.String("to", "", "interval end (exclusive)")
	eventsPath := fs.String("events", "", "optional client JSONL event log")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifestPath == "" {
		fmt.Fprintln(stderr, "medtrace trace: --manifest is required")
		return 2
	}
	from, err := parseDuration(*fromStr)
	if err != nil {
		fmt.Fprintf(stderr, "medtrace trace: --from: %v\n", err)
		return 2
	}
	to, err := parseDuration(*toStr)
	if err != nil {
		fmt.Fprintf(stderr, "medtrace trace: --to: %v\n", err)
		return 2
	}
	pl, err := manifest.Load(*root, *manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "medtrace trace: %v\n", err)
		return exitFor(err)
	}
	idx, scanAnomalies, err := index.Build(*root, pl)
	if err != nil {
		fmt.Fprintf(stderr, "medtrace trace: %v\n", err)
		return exitFor(err)
	}
	var eventAnomalies []core.Anomaly
	if *eventsPath != "" {
		log, err := events.ReadFile(*root, *eventsPath)
		if err != nil {
			fmt.Fprintf(stderr, "medtrace trace: %v\n", err)
			return exitFor(err)
		}
		eventAnomalies = events.Anomalies(log)
	}
	res, err := (&stream.Runner{Root: *root, Index: idx, VerifyHashes: true}).Run(from, to)
	if err != nil {
		fmt.Fprintf(stderr, "medtrace trace: %v\n", err)
		return exitFor(err)
	}
	res.Anomalies = append(res.Anomalies, eventAnomalies...)
	rep := report.Build(from, to, idx, idx.Epochs, scanAnomalies, res)
	rep.Recount()
	if err := rep.Write(stdout); err != nil {
		fmt.Fprintf(stderr, "medtrace trace: write report: %v\n", err)
		return 3
	}
	return 0
}

// exitFor maps an error to the exit-code contract.
func exitFor(err error) int {
	return core.ExitCode(core.KindOf(err))
}

// parseDuration parses a duration string accepted by time.ParseDuration, or a
// bare integer number of seconds.
func parseDuration(s string) (core.Nanos, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration")
	}
	return core.Nanos(d), nil
}
