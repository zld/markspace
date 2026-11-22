// avcheck is the acceptance entry point for the media kernel: it generates
// synthetic captures, injects transport faults, recovers, composes and
// exports, then prints machine-checkable metrics.
package main

import (
	"context"
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
	// The rerun exists only to prove reproducibility; its frames are removed
	// so the requested output directory is the sole artifact.
	os.RemoveAll(rerunDir)
	matched, verr := render.VerifySegments(cfg.OutDir, res.Manifest)
	if verr != nil {
		fmt.Fprintln(os.Stderr, "verify segments:", verr)
		return 1
	}
	summary := session.Summarize(res)
	summary.RerunManifestMatch = rerunMatch
	summary.SegmentVerified = matched
	summary.ElapsedMS = time.Since(begin).Milliseconds()
	summary.Exit = demoExit(res)
	if err := session.SaveRun(*out, rc); err != nil {
		fmt.Fprintln(os.Stderr, "save run:", err)
		return 1
	}
	if err := session.WriteSummary(*out, summary); err != nil {
		fmt.Fprintln(os.Stderr, "save summary:", err)
		return 1
	}
	printSummary(summary)
	if res.Faults.Corrupted > 0 || res.Faults.Truncated > 0 {
		fmt.Println("recovered=corrupt-input")
	}
	return summary.Exit
}

// verifyCmd re-checks an existing export directory from disk. The default
// path is independent of the producing run: it recomputes every hash from the
// PNGs, re-reads each MP4 sample table to measure the keyframe hit rate, and
// compares the manifest digest with the recorded summary. --rerun additionally
// re-executes the whole pipeline for a byte-for-byte comparison.
func verifyCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "export directory (required)")
	rerun := fs.Bool("rerun", false, "re-execute from run.json and compare the manifest")
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
	rc, err := session.LoadRun(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify: no run.json:", err)
		return 1
	}
	if !sameConfig(rc, man) {
		fmt.Fprintln(os.Stderr, "verify: run.json does not describe this manifest")
		return 1
	}
	sum, err := session.LoadSummary(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify: no summary.json:", err)
		return 1
	}
	hits, lookups, err := session.KeyRateFromIndex(*input, cfgTimescale(rc))
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify keyframe index:", err)
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
	fmt.Printf("roundtrip_consistency=%.3f\n", sum.RoundtripConsistency)
	fmt.Printf("keyframe_index_hit_rate=%.3f\n", session.HitRatio(hits, lookups))
	if sum.ManifestDigest != man.Digest {
		fmt.Println("verify: summary digest disagrees with the manifest")
		return 1
	}
	if matched != len(man.Segments) || frameMatch != man.Frames {
		fmt.Println("verify: export does not match its manifest")
		return 1
	}
	if *rerun {
		cfg := rc.Config
		faults, err := session.ParseFaults(rc.Faults)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify:", err)
			return 1
		}
		cfg.OutDir = *input + ".verify-rerun"
		res, err := session.Run(ctx, cfg, faults)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify rerun:", err)
			return 1
		}
		consistent := res.Manifest.Digest == man.Digest && res.Manifest.Summary == man.Summary
		fmt.Printf("rerun_manifest_match=%t\n", consistent)
		os.RemoveAll(cfg.OutDir)
		if !consistent {
			fmt.Println("verify: rerun does not reproduce the manifest")
			return 1
		}
	}
	fmt.Println("verify: ok")
	if man.Inputs.Damaged() {
		return 2
	}
	return 0
}

// cfgTimescale returns the clock timescale recorded in the run, defaulting to
// the acceptance value when the field is absent.
func cfgTimescale(rc session.RunConfig) int64 {
	if rc.Config.Timescale > 0 {
		return rc.Config.Timescale
	}
	return 90000
}

// sameConfig checks that run.json plausibly describes the manifest: the
// track count, frame count and segment layout must agree, so a stale run.json
// copied next to an unrelated export is rejected instead of silently rerun.
func sameConfig(rc session.RunConfig, man *render.Manifest) bool {
	c := rc.Config
	if c.Frames <= 0 || c.Tracks <= 0 || c.Tracks != man.Tracks || c.Frames != man.Frames {
		return false
	}
	seg := c.SegFrames
	if seg <= 0 {
		seg = c.Frames / 12
	}
	if seg < 1 {
		seg = 1
	}
	want := (c.Frames + seg - 1) / seg
	return want == len(man.Segments)
}

// demoExit reports demo success. The demo's purpose is to demonstrate the
// pipeline, so a run that completes with recovery is a success (0); exit 2 is
// reserved for verify, which inspects a finished export. Structural failures
// return 1 earlier.
func demoExit(r *session.Result) int {
	if r.Manifest == nil || r.Manifest.Frames == 0 {
		return 1
	}
	if r.RoundtripConsistency() < 1 {
		return 1
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
