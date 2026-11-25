// Command continuity is the terminal acceptance entry point for the media
// continuity engine. It generates local fixtures, runs the pipeline and prints
// a stable summary. Exit codes: 0 success, 1 usage, 2 input or boundary
// rejection.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	continuity "continuity/internal"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: continuity demo [flags]")
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usageText)
		return 1
	}
}

const usageText = `continuity demo --format hls,dash --late-ms 500 --queue-bytes 1048576 [--fault NAME]

flags:
  --format       comma-separated manifest formats: hls, dash
  --late-ms      late-data window in milliseconds (default 500)
  --queue-bytes  streaming buffer byte budget (default 1048576)
  --fault        none | truncated-box | late-with-checkpoint
  --workdir      directory for generated fixtures and checkpoints
  --keep         keep the generated fixture directory and print its path
`

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	formatFlag := fs.String("format", "hls,dash", "comma-separated manifest formats")
	lateMS := fs.Int64("late-ms", 500, "late window in milliseconds")
	queueBytes := fs.Int64("queue-bytes", continuity.DefaultQueueBytes, "queue byte budget")
	faultFlag := fs.String("fault", "none", "fault scenario: none, truncated-box, late-with-checkpoint")
	workDir := fs.String("workdir", "", "fixture/checkpoint directory (default: temporary)")
	keep := fs.Bool("keep", false, "keep the generated fixture directory")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "usage error:", err)
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 1
	}
	formats, err := parseFormats(*formatFlag)
	if err != nil {
		fmt.Fprintln(stderr, "usage error:", err)
		return 1
	}
	if *lateMS < 0 {
		fmt.Fprintln(stderr, "usage error: --late-ms must be non-negative")
		return 1
	}
	if *queueBytes <= 0 {
		fmt.Fprintln(stderr, "usage error: --queue-bytes must be positive")
		return 1
	}
	lateTicks, err := continuity.FromMillis(*lateMS)
	if err != nil {
		fmt.Fprintln(stderr, "usage error: --late-ms out of range")
		return 1
	}
	fault, err := parseFault(*faultFlag)
	if err != nil {
		fmt.Fprintln(stderr, "usage error:", err)
		return 1
	}

	dir := *workDir
	cleanup := func() {}
	if dir == "" {
		tmp, err := os.MkdirTemp("", "continuity-demo-")
		if err != nil {
			fmt.Fprintln(stderr, "cannot create temporary directory:", err)
			return 1
		}
		dir = tmp
		if !*keep {
			cleanup = func() { os.RemoveAll(tmp) }
		}
	}
	defer cleanup()

	demoCfg := continuity.DefaultDemoConfig()
	if fault == continuity.FaultTruncatedBox {
		demoCfg.TruncateBackup = true
	}
	if fault == continuity.FaultLateCheckpoint {
		demoCfg = continuity.SwitchoverDemoConfig()
	}
	fx, err := continuity.GenerateDemo(dir, demoCfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return continuity.ExitCode(err)
	}

	report, err := continuity.Run(context.Background(), continuity.EngineConfig{
		Root:       dir,
		Format:     formats,
		LateWindow: lateTicks,
		QueueBytes: *queueBytes,
		Fault:      fault,
		WorkDir:    dir,
	})
	if *keep {
		defer fmt.Fprintf(stdout, "fixtures: %s\n", dir)
	}
	if err != nil {
		printFault(stdout, err)
		return continuity.ExitCode(err)
	}
	printReport(stdout, report, fx)
	return 0
}

func parseFormats(s string) ([]continuity.ManifestKind, error) {
	var out []continuity.ManifestKind
	seen := map[continuity.ManifestKind]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		var kind continuity.ManifestKind
		switch part {
		case "hls":
			kind = continuity.ManifestHLS
		case "dash":
			kind = continuity.ManifestDASH
		default:
			return nil, fmt.Errorf("unknown format %q (want hls or dash)", part)
		}
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no format selected")
	}
	return out, nil
}

func parseFault(s string) (continuity.FaultKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return continuity.FaultNone, nil
	case "truncated-box":
		return continuity.FaultTruncatedBox, nil
	case "late-with-checkpoint":
		return continuity.FaultLateCheckpoint, nil
	default:
		return "", fmt.Errorf("unknown fault %q (want none, truncated-box or late-with-checkpoint)", s)
	}
}

// printFault emits the same summary keys as a successful run so a failing
// acceptance check can be parsed identically. The digest is a dash because
// nothing was committed.
func printFault(w io.Writer, err error) {
	fmt.Fprintf(w, "samples=0\n")
	fmt.Fprintf(w, "late_recomputed=0\n")
	fmt.Fprintf(w, "late_rejected=0\n")
	fmt.Fprintf(w, "time_delta_ticks=0\n")
	fmt.Fprintf(w, "byte_delta=0\n")
	fmt.Fprintf(w, "failure_location=%s\n", continuity.FailureLocation(err))
	fmt.Fprintf(w, "sha256=%s\n", "-")
	fmt.Fprintf(w, "error=%s\n", err)
	fmt.Fprintf(w, "exit=%d\n", continuity.ExitCode(err))
}

func printReport(w io.Writer, rep *continuity.Report, fx *continuity.DemoFixture) {
	fmt.Fprintf(w, "fixtures_root=%s\n", fx.Root)
	fmt.Fprintf(w, "formats=%s\n", joinFormats(rep.Formats))
	for _, track := range rep.Tracks {
		fmt.Fprintf(w, "track %s codec=%s timescale=%d\n", track.Key, track.Codec, track.Timescale)
	}
	for _, line := range rep.MappingLines {
		fmt.Fprintf(w, "mapping %s\n", line)
	}
	if len(rep.Lines) > 0 {
		for _, line := range rep.Lines {
			fmt.Fprintf(w, "%s\n", line)
		}
	}
	fmt.Fprintf(w, "samples=%d\n", rep.Samples)
	fmt.Fprintf(w, "late_recomputed=%d\n", rep.LateRecomputed)
	fmt.Fprintf(w, "late_rejected=%d\n", rep.LateRejected)
	fmt.Fprintf(w, "time_delta_ticks=%d\n", rep.TimeDeltaTicks)
	fmt.Fprintf(w, "byte_delta=%d\n", rep.ByteDelta)
	fmt.Fprintf(w, "mapping_missing=%d\n", rep.MappingMissing)
	fmt.Fprintf(w, "segments=%d\n", rep.Segments)
	fmt.Fprintf(w, "bytes=%d\n", rep.Bytes)
	fmt.Fprintf(w, "gaps=%d\n", rep.Gaps)
	fmt.Fprintf(w, "duplicates=%d\n", rep.Duplicates)
	fmt.Fprintf(w, "overlaps_dropped=%d\n", rep.OverlapsDropped)
	fmt.Fprintf(w, "track_map_sha256=%s\n", rep.TrackMapDigest)
	fmt.Fprintf(w, "failure_location=%s\n", rep.FailureLocation)
	fmt.Fprintf(w, "sha256=%s\n", rep.SHA256)
	fmt.Fprintf(w, "exit=0\n")
}

func joinFormats(kinds []continuity.ManifestKind) string {
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, ",")
}
