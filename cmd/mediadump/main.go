// Command mediadump generates a self-contained RTP/RTCP scenario, runs it
// through the media kernel, and prints the delivery report.
//
// It reads no external media files and starts no services. Exit codes are part
// of the contract:
//
//	0  the demo ran and every invariant held
//	1  media damage or sync problems were correctly recorded, but a delivery
//	   check failed
//	2  bad parameters, an unsupported adapter version, or a command error
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"example.com/mediaforge/internal/merrors"
	"example.com/mediaforge/internal/pipeline"
	"example.com/mediaforge/internal/segment"
)

const usage = `mediadump - local media kernel demo

Usage:
  mediadump --generate [flags]

Flags:
  --generate        build the scenario and run it (required)
  --seed N          generator seed (default 41)
  --loss N          single-packet losses, each parity-protected (default 1)
  --reorder N       packets delivered out of order (default 7)
  --drift-ppm N     device clock drift in ppm (default 120)
  --wrap-at N       32-bit timestamp at which the video clock wraps
  --vfr             variable frame rate video (default true)
  --segments N      number of deliverable segments (default 12)

The command exits 0 when the demo and all invariants pass, 1 when media
damage or sync problems were recorded and a delivery check failed, and 2 for
parameter, adapter-version, or usage errors.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// exit codes.
const (
	exitOK       = 0
	exitDelivery = 1
	exitUsage    = 2
)

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mediadump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }

	generate := fs.Bool("generate", false, "build the scenario and run it")
	seed := fs.Int64("seed", 41, "generator seed")
	loss := fs.Int("loss", 1, "single-packet losses, each parity-protected")
	reorder := fs.Int("reorder", 7, "packets delivered out of order")
	drift := fs.Int("drift-ppm", 120, "device clock drift in ppm")
	wrapAt := fs.Uint64("wrap-at", 4294967000, "32-bit timestamp at which the video clock wraps")
	vfr := fs.Bool("vfr", true, "variable frame rate video")
	segments := fs.Int("segments", 12, "number of deliverable segments")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if !*generate {
		fmt.Fprintln(stderr, "error: --generate is required")
		fs.Usage()
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}
	if *wrapAt > 0xFFFFFFFF {
		fmt.Fprintf(stderr, "error: --wrap-at %d exceeds 32-bit timestamp range\n", *wrapAt)
		return exitUsage
	}
	if *drift < -100_000 || *drift > 100_000 {
		fmt.Fprintf(stderr, "error: --drift-ppm %d outside +-100000\n", *drift)
		return exitUsage
	}
	if *segments <= 0 {
		fmt.Fprintf(stderr, "error: --segments %d must be positive\n", *segments)
		return exitUsage
	}
	if *loss < 0 || *reorder < 0 {
		fmt.Fprintln(stderr, "error: --loss and --reorder must be non-negative")
		return exitUsage
	}

	cfg := pipeline.DefaultScenarioConfig()
	cfg.Seed = *seed
	cfg.Loss = *loss
	cfg.Reorder = *reorder
	cfg.DriftPPM = *drift
	cfg.WrapAt = uint32(*wrapAt)
	cfg.VFR = *vfr
	cfg.Segments = *segments
	// The loss plan needs distinct sequence numbers for each loss.
	if *loss > 1 {
		cfg.VideoPackets = 160 + 8*(*loss-1)
	}

	sc, err := pipeline.Generate(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitUsage
	}
	rep, err := pipeline.Process(sc.Input)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		if stage, ok := merrors.StageOf(err); ok && stage == merrors.StageAdapter {
			// An unsupported version or codec is an environment/usage
			// problem, not a delivery failure.
			return exitUsage
		}
		return exitDelivery
	}

	printReport(stdout, rep)

	if failures := checkInvariants(rep, sc); len(failures) > 0 {
		fmt.Fprintln(stdout, "FAIL")
		for _, f := range failures {
			fmt.Fprintf(stdout, "  - %s\n", f)
		}
		return exitDelivery
	}
	fmt.Fprintln(stdout, "PASS")
	return exitOK
}

// printReport writes the demo's terminal report, one field per line, with the
// segment digests listed afterwards.
func printReport(w io.Writer, rep *pipeline.Report) {
	matched := 0
	for _, h := range rep.SegmentHashes {
		var zero [32]byte
		if h.SHA256 != zero {
			matched++
		}
	}
	fmt.Fprintf(w, "roundtrip_rate=%.4f\n", clamp01(rep.RoundtripRate))
	fmt.Fprintf(w, "keyframe_hit_rate=%.4f\n", clamp01(rep.KeyframeHitRate))
	fmt.Fprintf(w, "segments=%d\n", rep.SegmentCount)
	fmt.Fprintf(w, "segment_sha256=%d/%d\n", matched, rep.SegmentCount)
	fmt.Fprintf(w, "recovered_packets=%d\n", rep.Recovered)
	fmt.Fprintf(w, "unrecovered_gaps=%d\n", rep.UnrecoveredGaps)
	fmt.Fprintf(w, "late_packets=%d\n", rep.Late)
	fmt.Fprintf(w, "duplicate_packets=%d\n", rep.Duplicates)
	fmt.Fprintf(w, "clock_wraps=%d\n", rep.ClockWraps)
	fmt.Fprintf(w, "drift_ppm_est=%d\n", rep.DriftPPM)
	fmt.Fprintf(w, "truncated_errors=%d\n", rep.Truncated)
	fmt.Fprintf(w, "out_of_sync_samples=%d\n", rep.OutOfSync)
	fmt.Fprintf(w, "delivered_samples=%d/%d\n", rep.DeliveredSamples, rep.ExpectedSamples)
	fmt.Fprintf(w, "remux_bytes=%d\n", rep.RemuxBytes)
	for _, h := range rep.SegmentHashes {
		fmt.Fprintf(w, "segment[%02d] %s ticks=[%d,%d) samples=%d bytes=%d\n",
			h.Index, h.Hex(), h.StartTick, h.EndTick, h.Samples, h.Bytes)
	}
	if len(rep.Errors) > 0 {
		fmt.Fprintf(w, "recorded_errors=%d\n", len(rep.Errors))
		// Errors are already sorted by the pipeline, so repeated runs print
		// identical diagnostics.
		shown := rep.Errors
		if len(shown) > 12 {
			shown = shown[:12]
		}
		for _, e := range shown {
			fmt.Fprintf(w, "  error: %v\n", e)
		}
		if len(rep.Errors) > len(shown) {
			fmt.Fprintf(w, "  ... %d more\n", len(rep.Errors)-len(shown))
		}
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// checkInvariants encodes the acceptance criteria. It returns human-readable
// failures; an empty slice means delivery passed.
func checkInvariants(rep *pipeline.Report, sc *pipeline.Scenario) []string {
	var f []string
	add := func(cond bool, format string, args ...any) {
		if !cond {
			f = append(f, fmt.Sprintf(format, args...))
		}
	}
	add(rep.RoundtripRate >= 0.99 && rep.RoundtripRate <= 1.0,
		"roundtrip_rate %.4f outside 0.99..1.00", rep.RoundtripRate)
	add(rep.KeyframeHitRate >= 0.95 && rep.KeyframeHitRate <= 1.0,
		"keyframe_hit_rate %.4f outside 0.95..1.00", rep.KeyframeHitRate)
	add(rep.SegmentCount == sc.Config.Segments,
		"segments %d != requested %d", rep.SegmentCount, sc.Config.Segments)
	add(len(rep.SegmentHashes) == rep.SegmentCount && rep.SegmentCount > 0,
		"segment_sha256 %d/%d", len(rep.SegmentHashes), rep.SegmentCount)
	for _, h := range rep.SegmentHashes {
		var zero [32]byte
		add(h.SHA256 != zero, "segment %d has no digest", h.Index)
	}
	add(rep.Recovered == sc.ExpectedRecovered,
		"recovered_packets %d != expected %d", rep.Recovered, sc.ExpectedRecovered)
	add(rep.UnrecoveredGaps >= 1, "unrecovered_gaps %d < 1", rep.UnrecoveredGaps)
	add(rep.Late >= 1, "late_packets %d < 1", rep.Late)
	add(rep.ClockWraps >= 1, "clock_wraps %d < 1", rep.ClockWraps)
	add(rep.DriftPPM >= 100 && rep.DriftPPM <= 140,
		"drift_ppm_est %d outside 100..140", rep.DriftPPM)
	add(rep.Truncated == sc.ExpectedTruncated,
		"truncated_errors %d != expected %d", rep.Truncated, sc.ExpectedTruncated)
	add(rep.OutOfSync == 0, "out_of_sync_samples %d != 0", rep.OutOfSync)
	add(rep.DeliveredSamples > 0 && rep.RemuxBytes > 0, "no deliverable output")

	// Cross-stage consistency: the hashes must cover exactly the segments and
	// the digest list must be strictly ordered.
	if _, ok := segment.Equal(rep.SegmentHashes, rep.SegmentHashes); !ok {
		f = append(f, "segment hash list is not comparable to itself")
	}
	ordered := sort.SliceIsSorted(rep.SegmentHashes, func(i, j int) bool {
		return rep.SegmentHashes[i].StartTick < rep.SegmentHashes[j].StartTick
	})
	add(ordered, "segment boundaries are not monotonic")
	return f
}
