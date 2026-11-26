// Command assetpack builds, verifies, ranges, and rolls back local media packs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"assetpack/internal/clock"
	"assetpack/internal/demo"
	"assetpack/internal/release"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(ctx, args[1:])
	case "rollback":
		return runRollback(ctx, args[1:])
	case "verify":
		return runVerify(args[1:])
	case "range", "read-range":
		return runRange(ctx, args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func runDemo(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "output root (required)")
	sequence := fs.Int("sequence", 1, "deterministic sequence number")
	rangeSpec := fs.String("range", "45000:135000", "tick interval from:to (left closed, right open)")
	from := fs.Int64("from", -1, "range start in 90kHz ticks")
	to := fs.Int64("to", -1, "range end in 90kHz ticks")
	queueBlocks := fs.Int("queue-blocks", 2, "maximum queued blocks")
	slowMS := fs.Int("slow-consumer-ms", 0, "per-block consumer delay")
	fault := fs.String("fault", "", "fault injection: bad-mdat|rtp-duplicate|rtp-missing|unknown-version|publish|cancel")
	cancelAfter := fs.Duration("cancel-after", 0, "cancel after this duration (test hook)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "--out is required")
		return 2
	}
	r, err := parseRange(*rangeSpec, *from, *to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	res, err := demo.Run(ctx, demo.Options{
		Out: *out, Sequence: *sequence, Range: r, QueueBlocks: *queueBlocks,
		SlowConsumerMS: *slowMS, Fault: *fault, CancelAfter: *cancelAfter,
	})
	if err != nil {
		return reportError(err)
	}
	printDemoReport(res)
	return 0
}

func runRollback(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "release root")
	to := fs.String("to", "previous", "target version or 'previous'")
	verify := fs.Bool("verify", false, "recompute hashes and pixel/audio metrics")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "--root is required")
		return 2
	}
	m, st, err := release.Rollback(ctx, *root, *to, *verify)
	if err != nil {
		return reportError(err)
	}
	fmt.Printf("rollback=ok root=%s current=%s previous=%s verified=%t sequence=%d\n",
		*root, st.Current, st.Previous, *verify, m.Sequence)
	return 0
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "release root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "--root is required")
		return 2
	}
	current := filepath.Join(*root, release.CurrentLink)
	m, err := release.VerifyWithPixels(current)
	if err != nil {
		return reportError(err)
	}
	fmt.Printf("verify=ok version=%s sequence=%d artifacts=%d pixel_check=recomputed_match\n",
		m.Version, m.Sequence, len(m.Artifacts))
	for _, t := range m.Targets {
		fmt.Printf("verified_rgb platform=%s r=%.6f g=%.6f b=%.6f\n", t.Platform, t.RGBMean.R, t.RGBMean.G, t.RGBMean.B)
	}
	return 0
}

func runRange(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("range", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "release root")
	from := fs.Int64("from", -1, "start tick")
	to := fs.Int64("to", -1, "end tick")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" || *from < 0 || *to < 0 {
		fmt.Fprintln(os.Stderr, "range requires --root --from N --to N")
		return 2
	}
	cov, err := demo.QueryRange(ctx, *root, clock.Range{From: clock.Ticks(*from), To: clock.Ticks(*to)})
	if err != nil {
		return reportError(err)
	}
	fmt.Printf("range=ok requested=%d:%d actual=%d:%d records=%d preroll=%d alignment=%d/1 sample\n",
		cov.Requested.From, cov.Requested.To, cov.Actual.From, cov.Actual.To,
		len(cov.Records), len(cov.Preroll), cov.MaxRoundedAlignSamples)
	return 0
}

func parseRange(spec string, from, to int64) (clock.Range, error) {
	if from >= 0 || to >= 0 {
		if from < 0 || to < 0 {
			return clock.Range{}, fmt.Errorf("--from and --to must be used together")
		}
		return clock.Range{From: clock.Ticks(from), To: clock.Ticks(to)}, nil
	}
	parts := strings.Split(spec, ":")
	if len(parts) != 2 {
		return clock.Range{}, fmt.Errorf("invalid --range %q, want from:to", spec)
	}
	f, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return clock.Range{}, fmt.Errorf("invalid range start: %w", err)
	}
	t, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return clock.Range{}, fmt.Errorf("invalid range end: %w", err)
	}
	return clock.Range{From: clock.Ticks(f), To: clock.Ticks(t)}, nil
}

func printDemoReport(res *demo.Result) {
	m := res.Commit.Manifest
	desktop := m.Targets[0]
	mobile := m.Targets[1]
	fmt.Printf("status=success sequence=%d version=%s reused=%t\n", m.Sequence, m.Version, res.Commit.Reused)
	fmt.Printf("timebase_hz=%d\n", m.TimebaseHz)
	fmt.Printf("desktop_frames=%d\n", res.DesktopCount)
	fmt.Printf("mobile_frames=%d\n", res.MobileCount)
	fmt.Printf("audio_sample_frames=%d\n", res.AudioSamples)
	fmt.Printf("rgb_mean_desktop r=%.2f g=%.2f b=%.2f channel_range=0-255\n",
		desktop.RGBMean.R, desktop.RGBMean.G, desktop.RGBMean.B)
	fmt.Printf("rgb_mean_mobile r=%.2f g=%.2f b=%.2f channel_range=0-255\n",
		mobile.RGBMean.R, mobile.RGBMean.G, mobile.RGBMean.B)
	fmt.Printf("audio_peak_dbfs=%.2f reference=-12.04 tolerance=±0.1\n", desktop.PeakDBFS)
	fmt.Printf("对齐误差: %d/1 sample actual=%d:%d requested=%d:%d\n",
		desktop.AlignmentErrorSamples, desktop.ActualRange.From, desktop.ActualRange.To, m.Range.From, m.Range.To)
	fmt.Printf("queue_peak=%d queue_capacity=%d peak_limit=2\n", res.Stats.QueuePeak, res.Stats.QueueCap)
	fmt.Printf("frame_sha256 file=desktop/frame-00012.rgba sha256=%s\n", res.Frame12Hash)
	for _, t := range m.Targets {
		for _, s := range t.Segments {
			fmt.Printf("segment_sha256 platform=%s name=%s frames=%d sha256=%s\n", t.Platform, s.Name, s.FrameCount, s.SHA256)
		}
	}
	fmt.Printf("input_sha256 name=%s sha256=%s\n", m.Inputs[0].Name, m.Inputs[0].SHA256)
	fmt.Printf("release_dir=%s current=%s\n", res.Commit.VersionDir, filepath.Join(filepath.Dir(res.Commit.VersionDir), release.CurrentLink))
}

func reportError(err error) int {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 3
	case errors.Is(err, release.ErrPublish), errors.Is(err, release.ErrRollbackFailed), errors.Is(err, release.ErrVerification):
		return 4
	default:
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, `assetpack commands:
  demo --out DIR [--sequence 1|2] [--range 45000:135000] [--from N --to N]
       [--queue-blocks 2] [--slow-consumer-ms 5]
       [--fault bad-mdat|rtp-duplicate|rtp-missing|unknown-version|publish|cancel]
  range --root DIR --from N --to N
  verify --root DIR
  rollback --root DIR --to previous [--verify]

exit codes: 0 ok, 2 input/range error, 3 canceled, 4 publish/rollback failure`)
}
