// Command archivecheck demonstrates and verifies the archive recovery kernel.
//
// Exit codes: 0 for a passing acceptance sample, 2 when input damage was
// located and recovery completed, 1 for parameter or internal errors.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"archivecheck/internal/pipeline"
	"archivecheck/internal/sample"
)

const (
	exitOK      = 0
	exitError   = 1
	exitRecover = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "archivecheck: unknown command %q\n", args[0])
		usage(stderr)
		return exitError
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: archivecheck demo [flags]

flags:
  --case NAME            fixture case: ok, split-startcode, corrupt-index,
                         obu-truncated, keyframe-regress, boundary-skew
  --seed N               deterministic generator seed (default 23)
  --chunk-bytes N        bounded read chunk size (default 7)
  --queue N              per-layer queue depth (default 2)
  --target-kbps A,B,C    low,mid,high target bitrates (default 900,1800,3600)
  --consumer-delay-ms N  artificial consumer delay for backpressure (default 15)
  --tolerance-ms N       shared-clock boundary tolerance (default 2)
  --window-ms N          bitrate sliding window (default 250)
  --max-keyframe-interval-ms N  keyframe cadence limit (default 300, 0 disables)
  --json                 emit the manifest as JSON after the text summary
`)
}

type demoFlags struct {
	caseName   string
	seed       int64
	chunkBytes int
	queue      int
	targets    []float64
	delayMs    int
	tolerance  int64
	windowMs   int64
	maxKFMs    int64
	json       bool
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f demoFlags
	targetText := "900,1800,3600"
	fs.StringVar(&f.caseName, "case", sample.CaseOK, "fixture case")
	fs.Int64Var(&f.seed, "seed", 23, "generator seed")
	fs.IntVar(&f.chunkBytes, "chunk-bytes", 7, "read chunk size")
	fs.IntVar(&f.queue, "queue", 2, "per-layer queue depth")
	fs.StringVar(&targetText, "target-kbps", targetText, "low,mid,high target bitrates")
	fs.IntVar(&f.delayMs, "consumer-delay-ms", 15, "consumer delay in milliseconds")
	fs.Int64Var(&f.tolerance, "tolerance-ms", 2, "boundary tolerance in milliseconds")
	fs.Int64Var(&f.windowMs, "window-ms", 250, "bitrate window in milliseconds")
	fs.Int64Var(&f.maxKFMs, "max-keyframe-interval-ms", 300, "keyframe cadence limit; 0 disables")
	fs.BoolVar(&f.json, "json", false, "emit manifest JSON")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "archivecheck: unexpected arguments %q\n", fs.Args())
		return exitError
	}
	var err error
	if f.targets, err = parseTargets(targetText); err != nil {
		fmt.Fprintf(stderr, "archivecheck: %v\n", err)
		return exitError
	}
	if f.chunkBytes <= 0 || f.queue <= 0 || f.delayMs < 0 || f.tolerance <= 0 || f.windowMs <= 0 || f.maxKFMs < 0 {
		fmt.Fprintf(stderr, "archivecheck: chunk-bytes, queue, tolerance-ms and window-ms must be positive; delay must be non-negative\n")
		return exitError
	}

	cfg := pipeline.Config{
		Case: f.caseName, Seed: f.seed, ChunkBytes: f.chunkBytes, Queue: f.queue,
		Targets: f.targets, ConsumerDelayMs: f.delayMs, ToleranceMs: f.tolerance,
		WindowMs: f.windowMs, MaxKeyframeIntervalMs: f.maxKFMs,
	}
	out, err := pipeline.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(stderr, "archivecheck: %v\n", err)
		return exitError
	}
	fmt.Fprint(stdout, out.Manifest.Text())
	if f.json {
		b, err := out.Manifest.JSON()
		if err != nil {
			fmt.Fprintf(stderr, "archivecheck: %v\n", err)
			return exitError
		}
		fmt.Fprintln(stdout, string(b))
	}
	fmt.Fprint(stdout, out.Verify.Text())
	fmt.Fprintf(stdout, "mp4 tracks=%d bytes=%d anchor_mismatches=%d sha256=%s\n",
		out.MP4Tracks, out.MP4Bytes, out.MetadataMismatches, out.MP4SHA256)
	fmt.Fprintf(stdout, "flow peak_buffered_chunks=%d queue_bound=%d goroutines_drained=%t\n",
		out.PeakBuffered, out.QueueBound, out.Drained)

	switch {
	case !out.Verify.OK():
		fmt.Fprintf(stderr, "archivecheck: invariants violated\n")
		return exitError
	case out.Damaged:
		fmt.Fprintf(stdout, "result=recovered exit=%d\n", exitRecover)
		return exitRecover
	default:
		fmt.Fprintf(stdout, "result=ok exit=%d\n", exitOK)
		return exitOK
	}
}

func parseTargets(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return nil, fmt.Errorf("target-kbps needs exactly three comma-separated values, got %d", len(parts))
	}
	out := make([]float64, 0, 3)
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || !(v > 0) {
			return nil, fmt.Errorf("invalid target bitrate %q", p)
		}
		out = append(out, v)
	}
	return out, nil
}
