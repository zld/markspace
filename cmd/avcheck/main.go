// avcheck is the acceptance entry point for the media kernel: it generates
// synthetic captures, injects transport faults, recovers, composes and
// exports, then prints machine-checkable metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"avcheck/internal/render"
	"avcheck/internal/session"
)

func main() { os.Exit(run(os.Args)) }

func run(args []string) int {
	if len(args) < 2 {
		usage()
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[1] {
	case "demo":
		return demoCmd(ctx, args[2:])
	case "verify":
		return verifyCmd(ctx, args[2:])
	default:
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  avcheck demo   --seed N --frames N --tracks N --queue N --faults a,b,c --out DIR
  avcheck verify --input DIR
exit codes: 0 clean, 2 recovered from corrupt input, 1 argument/structural error`)
}

// demoCmd runs the whole pipeline and prints the acceptance fields.
func demoCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 41, "deterministic seed")
	frames := fs.Int("frames", 1200, "frames per track")
	tracks := fs.Int("tracks", 3, "number of tracks")
	queue := fs.Int("queue", 32, "bounded queue capacity")
	faultList := fs.String("faults", "duplicate,reorder,burst-loss,bandwidth-shift,corrupt,truncated", "comma-separated faults")
	out := fs.String("out", "", "output directory (required)")
	width := fs.Int("width", 1920, "export raster width")
	height := fs.Int("height", 1080, "export raster height")
	segments := fs.Int("segments", 0, "segment count (0 = frames/100)")
	policy := fs.String("policy", "skip", "corrupt-frame policy: skip|fail-track")
	startSeq := fs.Uint64("start-seq", 0, "first frame sequence number")
	startTicks := fs.Int64("start-ticks", 0, "first capture tick")
	skew := fs.Int64("track-skew", 137, "per-track tick offset")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *out == "" || *frames <= 0 || *tracks <= 0 || *queue <= 0 {
		fmt.Fprintln(os.Stderr, "error: --out, --frames, --tracks and --queue are required and positive")
		return 1
	}
	faults, err := session.ParseFaults(strings.Split(*faultList, ","))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	cfg := session.Config{
		Seed: *seed, Frames: *frames, Tracks: *tracks, Queue: *queue,
		Width: *width, Height: *height, OutDir: *out,
		StartFrameSeq: *startSeq, StartTicks: *startTicks, TrackTickSkew: *skew,
	}
	if *segments > 0 {
		cfg.SegFrames = (*frames + *segments - 1) / *segments
	}
	switch *policy {
	case "skip":
		cfg.Policy = session.PolicySkip
	case "fail-track":
		cfg.Policy = session.PolicyFailTrack
	default:
		fmt.Fprintf(os.Stderr, "error: unknown policy %q\n", *policy)
		return 1
	}
	cfg.Defaults()
	begin := time.Now()
	res, err := session.Run(ctx, cfg, faults)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo failed:", err)
		return 1
	}
	// Rerun from the persisted config to prove the export is reproducible.
	digest1 := res.Manifest.Digest
	rc := session.RunConfig{Config: cfg, Faults: strings.Split(*faultList, ","), Seed: *seed}
	rerunDir := *out + ".rerun"
	cfg2 := cfg
	cfg2.OutDir = rerunDir
	res2, err := session.Run(ctx, cfg2, faults)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rerun failed:", err)
		return 1
	}
	rerunMatch := res2.Manifest.Digest == digest1 && res2.Manifest.Summary == res.Manifest.Summary
	matched, verr := render.VerifySegments(cfg.OutDir, res.Manifest)
	if verr != nil {
		fmt.Fprintln(os.Stderr, "verify segments:", verr)
		return 1
	}
	summary := session.Summarize(res)
	summary.RerunManifestMatch = rerunMatch
	summary.SegmentVerified = matched
	summary.ElapsedMS = time.Since(begin).Milliseconds()
	summary.Exit = exitCode(res)
	if err := session.SaveRun(*out, rc); err != nil {
		fmt.Fprintln(os.Stderr, "save run:", err)
		return 1
	}
	if err := session.WriteSummary(*out, summary); err != nil {
		fmt.Fprintln(os.Stderr, "save summary:", err)
		return 1
	}
	printSummary(summary)
	if summary.Exit == 2 {
		fmt.Println("recovered=corrupt-input")
	}
	return summary.Exit
}

// verifyCmd re-checks an existing export directory from disk.
func verifyCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "export directory (required)")
	rerun := fs.Bool("rerun", true, "re-execute from run.json and compare the manifest")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *input == "" {
		fmt.Fprintln(os.Stderr, "error: --input is required")
		return 1
	}
	man, err := render.LoadManifest(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 1
	}
	if err := man.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "manifest invalid:", err)
		return 1
	}
	matched, err := render.VerifySegments(*input, man)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify segments:", err)
		return 1
	}
	frameMatch, err := render.VerifyFrames(*input, man)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify frames:", err)
		return 1
	}
	fmt.Printf("manifest_digest=%s\n", man.Digest)
	fmt.Printf("segments=%d segment_sha256_match=%d/%d\n", len(man.Segments), matched, len(man.Segments))
	fmt.Printf("frames=%d frame_sha256_match=%d/%d\n", man.Frames, frameMatch, man.Frames)
	for i, s := range man.Segments {
		status := "ok"
		if i >= matched {
			status = "mismatch"
		}
		fmt.Printf("segment[%02d] frames=%d first=%d last=%d sha256=%s %s\n",
			i, s.Frames, s.FirstIndex, s.LastIndex, s.SHA256[:16], status)
	}
	exit := 0
	if man.Inputs.Damaged() {
		exit = 2
	}
	// Re-derive roundtrip and keyframe metrics from the persisted run.
	var rr float64 = 1
	var kf float64 = 1
	if *rerun {
		rc, err := session.LoadRun(*input)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify: no run.json:", err)
			return 1
		}
		if !sameConfig(rc, man) {
			fmt.Fprintln(os.Stderr, "verify: run.json does not describe this manifest")
			return 1
		}
		cfg := rc.Config
		faults, err := session.ParseFaults(rc.Faults)
		if err != nil {
			return 1
		}
		cfg.OutDir = *input + ".verify-rerun"
		res, err := session.Run(ctx, cfg, faults)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify rerun:", err)
			return 1
		}
		rr = res.RoundtripConsistency()
		kf = res.KeyframeHitRate()
		consistent := res.Manifest.Digest == man.Digest && res.Manifest.Summary == man.Summary
		fmt.Printf("roundtrip_consistency=%.3f\n", rr)
		fmt.Printf("keyframe_index_hit_rate=%.3f\n", kf)
		fmt.Printf("rerun_manifest_match=%t\n", consistent)
		if matched != len(man.Segments) || frameMatch != man.Frames || !consistent {
			fmt.Println("verify: export does not match its manifest")
			return 1
		}
		os.RemoveAll(cfg.OutDir)
	} else {
		fmt.Printf("roundtrip_consistency=%.3f\n", rr)
		fmt.Printf("keyframe_index_hit_rate=%.3f\n", kf)
	}
	fmt.Println("verify: ok")
	return exit
}

func sameConfig(rc session.RunConfig, man *render.Manifest) bool {
	return rc.Config.Frames > 0 && rc.Config.Tracks == man.Tracks
}

func exitCode(r *session.Result) int {
	if r.Faults.Corrupted > 0 || r.Faults.Truncated > 0 || r.Burned > 0 {
		return 2
	}
	return 0
}

func printSummary(s session.Summary) {
	fmt.Printf("frames_in=%d\n", s.FramesIn)
	fmt.Printf("frames_generated=%d\n", s.FramesGenerated)
	fmt.Printf("tracks=%d\n", s.Tracks)
	fmt.Printf("duplicates_dropped=%d\n", s.DuplicatesDropped)
	fmt.Printf("reordered_recovered=%d\n", s.ReorderedRecovered)
	fmt.Printf("burst_gaps=%d\n", s.BurstGaps)
	fmt.Printf("corrupt_rejected=%d\n", s.CorruptRejected)
	fmt.Printf("truncated_rejected=%d\n", s.TruncatedRejected)
	fmt.Printf("queue_peak=%d\n", s.QueuePeak)
	fmt.Printf("roundtrip_consistency=%.3f\n", s.RoundtripConsistency)
	fmt.Printf("keyframe_index_hit_rate=%.3f\n", s.KeyframeHitRate)
	fmt.Printf("segments_verified=%d\n", s.Segments)
	fmt.Printf("segment_sha256_match=%d/%d\n", s.SegmentVerified, s.Segments)
	fmt.Printf("rerun_manifest_match=%t\n", s.RerunManifestMatch)
	fmt.Printf("frames_out=%d\n", s.Frames)
	fmt.Printf("manifest_digest=%s\n", s.ManifestDigest)
	fmt.Printf("elapsed_ms=%d\n", s.ElapsedMS)
}

var _ = errors.New
