// Command mediacore is the terminal acceptance entry point for the reusable
// processing kernel. It generates its own MP4/MOV and RTP fixtures, runs the
// demux -> timebase/track-map -> digest-bound index -> streaming remux/hash
// pipeline, exercises transport recovery and prints stable metrics.
//
// Exit code 0 means every assertion passed; any processing or verification
// failure is printed as a classified error and yields a non-zero exit code.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	mediacore "mediacore"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: mediacore demo [--seed N] [--loss-burst N] [--reorder N]")
	fmt.Fprintln(w, "                     [--duplicate N] [--bandwidth kbps,kbps,...]")
	fmt.Fprintln(w, "                     [--repeat N] [--workdir DIR] [--json]")
}

type cliFlags struct {
	seed      int64
	lossBurst int
	reorder   int
	duplicate int
	bandwidth string
	repeat    int
	workdir   string
	asJSON    bool
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f cliFlags
	fs.Int64Var(&f.seed, "seed", 17, "deterministic fixture seed")
	fs.IntVar(&f.lossBurst, "loss-burst", 3, "consecutive RTP packets to drop")
	fs.IntVar(&f.reorder, "reorder", 4, "number of adjacent reorder swaps")
	fs.IntVar(&f.duplicate, "duplicate", 2, "number of duplicated RTP packets")
	fs.StringVar(&f.bandwidth, "bandwidth", "", "comma-separated bandwidth steps in kbit/s (pacing only)")
	fs.IntVar(&f.repeat, "repeat", 1, "run the scenario N times and require identical segment hashes")
	fs.StringVar(&f.workdir, "workdir", "", "directory for generated fixtures (default: temporary)")
	fs.BoolVar(&f.asJSON, "json", false, "also print the full report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if f.repeat < 1 {
		fmt.Fprintln(stderr, "--repeat must be >= 1")
		return 2
	}
	bandwidth, err := parseBandwidth(f.bandwidth)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --bandwidth: %v\n", err)
		return 2
	}

	baseDir := f.workdir
	cleanup := func() {}
	if baseDir == "" {
		d, err := os.MkdirTemp("", "mediacore-demo-")
		if err != nil {
			fmt.Fprintf(stderr, "cannot create workdir: %v\n", err)
			return 1
		}
		baseDir = d
		cleanup = func() { _ = os.RemoveAll(d) }
	}
	defer cleanup()

	// Repeat runs each get an isolated subdirectory so neither a warm cache
	// nor leftover artefacts can mask a determinism regression.
	var hashes []string
	var last *mediacore.DemoReport
	for i := 0; i < f.repeat; i++ {
		dir := baseDir
		if f.repeat > 1 {
			dir = fmt.Sprintf("%s/run-%d", baseDir, i)
		}
		opts := mediacore.DemoOptions{
			Seed:      f.seed,
			LossBurst: f.lossBurst,
			Reorder:   f.reorder,
			Duplicate: f.duplicate,
			Bandwidth: bandwidth,
			Repeat:    f.repeat,
			WorkDir:   dir,
			Output:    stdout,
		}
		runner := mediacore.NewDemoRunner(opts, stdout)
		report, err := runner.Run()
		if err != nil {
			fmt.Fprintf(stderr, "demo failed: %v\n", err)
			return classifiedExit(err)
		}
		last = report
		hashes = append(hashes, report.SegmentSHA)
		if report.ExitCode != 0 {
			printSummary(stdout, report, f.repeat, hashes)
			return report.ExitCode
		}
	}

	identical := allEqual(hashes)
	if f.repeat >= 2 {
		fmt.Fprintf(stdout, "repeat  runs=%d identical=%t\n", f.repeat, identical)
		for i, h := range hashes {
			fmt.Fprintf(stdout, "  hash[%d]=%s\n", i, h)
		}
	}
	last.Deterministic = identical && last.Deterministic
	printSummary(stdout, last, f.repeat, hashes)
	if f.asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(last); err != nil {
			fmt.Fprintf(stderr, "json encode failed: %v\n", err)
			return 1
		}
	}
	if !identical {
		fmt.Fprintln(stderr, "repeat runs produced different segment hashes")
		return 1
	}
	return 0
}

// printSummary emits the stable, greppable metrics the acceptance checks look
// for. Keys are printed verbatim on their own lines.
func printSummary(w io.Writer, r *mediacore.DemoReport, repeat int, hashes []string) {
	fmt.Fprintln(w, "---- acceptance summary ----")
	fmt.Fprintf(w, "roundtrip_rate=%.3f\n", r.RoundtripRate)
	fmt.Fprintf(w, "keyframe_hit_rate=%.3f\n", r.KeyframeHitRate)
	fmt.Fprintf(w, "segment_sha256=%s\n", r.SegmentSHA)
	fmt.Fprintf(w, "duplicate_count=%d\n", r.DuplicateCount)
	fmt.Fprintf(w, "missing_count=%d\n", r.MissingCount)
	fmt.Fprintf(w, "index_rebuilt=%t\n", r.IndexRebuilt)
	fmt.Fprintf(w, "repeat=%d hashes_match=%t\n", repeat, allEqual(hashes))
	fmt.Fprintf(w, "exit_code=%d\n", r.ExitCode)
}

func parseBandwidth(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer: %w", p, err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("bandwidth step %d must be positive", v)
		}
		out = append(out, v)
	}
	return out, nil
}

func allEqual(xs []string) bool {
	if len(xs) == 0 {
		return true
	}
	for _, x := range xs[1:] {
		if x != xs[0] {
			return false
		}
	}
	return true
}

// classifiedExit maps a classified mediacore failure to a stable non-zero
// code while leaving unexpected errors at 1.
func classifiedExit(err error) int {
	var e *mediacore.Error
	if errors.As(err, &e) {
		return 3
	}
	return 1
}
