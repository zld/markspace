// Command rtp-regress runs the offline RTP/MPEG-TS audio regression tool:
// it generates local fixtures, recovers a monotonic timeline from RTP/TS,
// normalizes audio through ffmpeg, and emits fMP4 plus a quality manifest.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"rtpregress/internal/app"
	"rtpregress/internal/fixture"
	"rtpregress/internal/ts"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("rtp-regress", flag.ContinueOnError)
	generateDir := fs.String("generate", "", "generate all fixture traces into DIR")
	in := fs.String("in", "", "input length-prefixed RTP trace")
	out := fs.String("out", "", "output directory")
	rate := fs.Int("rate", 48000, "target sample rate")
	channels := fs.Int("channels", 1, "target channel count")
	bitrate := fs.Int("bitrate", 96000, "target AAC bitrate")
	workers := fs.Int("workers", 4, "bounded worker count (1..16)")
	policyStr := fs.String("policy", "isolate", "fault policy: isolate|strict")
	cancelAfter := fs.Int("cancel-after", 0, "cancel after N new segments (>0)")
	resume := fs.Bool("resume", false, "resume from a saved checkpoint")
	if err := fs.Parse(args); err != nil {
		return app.ExitStrictInput
	}

	if *generateDir != "" {
		return generate(*generateDir)
	}
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "error: -in and -out are required (or use -generate)")
		fs.Usage()
		return app.ExitStrictInput
	}
	policy, ok := ts.ParsePolicy(*policyStr)
	if !ok {
		fmt.Fprintf(os.Stderr, "error: unknown policy %q\n", *policyStr)
		return app.ExitStrictInput
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := app.Run(ctx, app.Options{
		InPath: *in, OutDir: *out, Rate: *rate, Channels: *channels,
		Bitrate: *bitrate, Workers: *workers, Policy: policy,
		CancelAfter: *cancelAfter, Resume: *resume,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if res != nil && res.ExitCode == app.ExitStrictInput {
			return app.ExitStrictInput
		}
		return 1
	}
	if res.Canceled {
		fmt.Printf("canceled after checkpoint: %s (exit %d)\n",
			app.CheckpointPath(*out), app.ExitCanceled)
		return app.ExitCanceled
	}
	printMetrics(res)
	return app.ExitOK
}

// generate writes every named fixture trace into dir.
func generate(dir string) int {
	ctx := context.Background()
	ff, err := findFFmpeg()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, name := range fixture.OrderedCaseNames() {
		raw, gerr := fixture.BuildTrace(ctx, ff, fixture.Cases()[name])
		if gerr != nil {
			fmt.Fprintf(os.Stderr, "error generating %s: %v\n", name, gerr)
			return 1
		}
		path := filepath.Join(dir, name+".rtp")
		if werr := os.WriteFile(path, raw, 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "error writing %s: %v\n", path, werr)
			return 1
		}
		fmt.Printf("generated %s (%d bytes)\n", path, len(raw))
	}
	return app.ExitOK
}

// printMetrics emits the stable key=value quality evidence required by spec.
func printMetrics(res *app.RunResult) {
	m := res.Metrics
	fmt.Printf("bitrate_target=%d\n", m.BitrateTarget)
	fmt.Printf("bitrate_actual=%d\n", m.BitrateActual)
	fmt.Printf("bitrate_delta_pct=%.2f\n", m.BitrateDeltaPct)
	fmt.Printf("snr_db=%.2f\n", m.SNRDB)
	fmt.Printf("lufs=%.2f\n", m.LUFS)
	fmt.Printf("true_peak_dbtp=%.2f\n", m.TruePeakDBTP)
	fmt.Printf("manifest_max_error_ms=%.3f\n", m.ManifestMaxErrorMS)
	fmt.Printf("gaps=%d psi_events=%d pcr_wraps=%d drift_ppm=%d\n",
		res.Stats.Gaps, res.Stats.PSIEvents, res.Stats.PCRWraps, res.Stats.DriftPPM)
}

func findFFmpeg() (string, error) {
	return resolveBinary("ffmpeg")
}
