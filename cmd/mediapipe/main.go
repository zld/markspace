// Command mediapipe is the terminal acceptance adapter. It contains no media
// logic of its own: it parses flags, runs the pipeline and prints the
// quantified summary required by the acceptance checklist.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediapipe/internal/pipeline"
)

// Exit codes required by the acceptance contract.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	command       string
	seed          int64
	jitterMS      int
	lossBurst     int
	checkpointDir string
	cacheDir      string
	window        int
	holdTicks     int64
	reorderPermil int
	dupPermil     int
	lateRatio     int
	interrupt     int
	resume        bool
	json          bool
}

func parseFlags(args []string, stderr *os.File) (options, error) {
	var o options
	if len(args) == 0 {
		return o, errors.New("missing command (demo)")
	}
	o.command = args[0]
	fs := flag.NewFlagSet("mediapipe "+o.command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Int64Var(&o.seed, "seed", 17, "fixture generation seed (non-negative)")
	fs.IntVar(&o.jitterMS, "jitter-ms", 80, "network jitter bound in milliseconds [0,500]")
	fs.IntVar(&o.lossBurst, "loss-burst", 3, "contiguous packet loss burst per segment [0,8]")
	fs.StringVar(&o.checkpointDir, "checkpoint-dir", "./tmp/checkpoints", "checkpoint journal directory")
	fs.StringVar(&o.cacheDir, "cache-dir", "", "cache directory (default <checkpoint-dir>/cache)")
	fs.IntVar(&o.window, "window", 64, "reorder window in packets [1,256]")
	fs.Int64Var(&o.holdTicks, "hold-ticks", 27000, "playout hold in 90 kHz ticks [0,180000]")
	fs.IntVar(&o.reorderPermil, "reorder-permille", 120, "packet reorder probability [0,1000]")
	fs.IntVar(&o.dupPermil, "duplicate-permille", 40, "duplicate packet probability [0,1000]")
	fs.IntVar(&o.lateRatio, "late-ratio", 250, "share of losses retransmitted too late [0,1000]")
	fs.IntVar(&o.interrupt, "interrupt-after", 0, "simulate process death after N segments [0,6]")
	fs.BoolVar(&o.resume, "resume", false, "resume from the last complete checkpoint")
	fs.BoolVar(&o.json, "json", false, "emit the summary as JSON lines")
	if err := fs.Parse(args[1:]); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return o, nil
}

// tighten applies the requested fault seed to the scenario and the network
// model. The pipeline derives every fault decision from the seed, so no
// additional randomness enters the run.
func (o options) config() pipeline.Config {
	cfg := pipeline.DefaultConfig()
	cfg.Seed = o.seed
	cfg.JitterMS = o.jitterMS
	cfg.LossBurst = o.lossBurst
	cfg.Window = o.window
	cfg.HoldTicks = o.holdTicks
	cfg.ReorderPermille = o.reorderPermil
	cfg.DuplicatePermille = o.dupPermil
	cfg.LateRatio = o.lateRatio
	cfg.RecoverWaitTicks = maxInt64(o.holdTicks, 9000)
	cfg.CheckpointDir = o.checkpointDir
	cfg.CacheDir = o.cacheDir
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(o.checkpointDir, "cache")
	}
	cfg.InterruptAfter = o.interrupt
	cfg.Resume = o.resume
	return cfg
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func run(args []string, stdout, stderr *os.File) int {
	o, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mediapipe: %v\n", err)
		fmt.Fprintf(stderr, "usage: mediapipe demo [flags]\n")
		return exitUsage
	}
	switch o.command {
	case "demo":
	default:
		fmt.Fprintf(stderr, "mediapipe: unknown command %q\n", o.command)
		return exitUsage
	}
	cfg := o.config()
	summary, code := execute(cfg, o, stdout, stderr)
	if summary == nil {
		return code
	}
	printSummary(stdout, summary, o)
	if !summary.OK() {
		fmt.Fprintf(stderr, "mediapipe: acceptance thresholds not met (roundtrip=%.3f keyframe=%.3f)\n",
			summary.RoundtripMatchRate, summary.KeyframeIndexHitRate)
		return exitFailure
	}
	return exitOK
}

// execute runs the demo. Unless interrupted explicitly, it performs one full
// run so the summary reports the resume count honestly as zero.
func execute(cfg pipeline.Config, o options, stdout, stderr *os.File) (*pipeline.Summary, int) {
	runner, err := pipeline.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mediapipe: setup: %v\n", err)
		if errors.Is(err, pipeline.ErrInvalidConfig) {
			return nil, exitUsage
		}
		return nil, exitFailure
	}
	summary, err := runner.Run()
	if err != nil {
		if errors.Is(err, pipeline.ErrInterrupted) {
			fmt.Fprintf(stdout, "interrupted=true committed=%d\n", o.interrupt)
			fmt.Fprintf(stderr, "mediapipe: simulated interruption after %d commit(s)\n", o.interrupt)
			return nil, exitOK
		}
		fmt.Fprintf(stderr, "mediapipe: run: %v\n", err)
		return nil, exitFailure
	}
	return summary, exitOK
}

func printSummary(w *os.File, s *pipeline.Summary, o options) {
	if o.json {
		enc := json.NewEncoder(w)
		_ = enc.Encode(map[string]any{
			"segments": s.Segments, "roundtrip_match_rate": s.RoundtripMatchRate,
			"keyframe_index_hit_rate": s.KeyframeIndexHitRate, "segment_hashes": s.SegmentHashes,
			"reordered_packets": s.ReorderedPackets, "recovered_packets": s.RecoveredPackets,
			"unrecovered_gaps": s.UnrecoveredGaps, "cache_hits": s.CacheHits,
			"cache_rejected": s.CacheRejected, "checkpoint_resumes": s.CheckpointResumes,
		})
		return
	}
	fmt.Fprintf(w, "segments=%d\n", s.Segments)
	fmt.Fprintf(w, "roundtrip_match_rate=%.3f\n", s.RoundtripMatchRate)
	fmt.Fprintf(w, "keyframe_index_hit_rate=%.3f\n", s.KeyframeIndexHitRate)
	fmt.Fprintf(w, "reordered_packets=%d\n", s.ReorderedPackets)
	fmt.Fprintf(w, "recovered_packets=%d\n", s.RecoveredPackets)
	fmt.Fprintf(w, "unrecovered_gaps=%d\n", s.UnrecoveredGaps)
	fmt.Fprintf(w, "unrecovered_packets=%d\n", s.UnrecoveredPackets)
	fmt.Fprintf(w, "duplicates=%d\n", s.Duplicates)
	fmt.Fprintf(w, "late_dropped=%d\n", s.LateDropped)
	fmt.Fprintf(w, "recovery_requests=%d\n", s.RecoveryRequests)
	fmt.Fprintf(w, "cache_hits=%d\n", s.CacheHits)
	fmt.Fprintf(w, "cache_rejected=%d\n", s.CacheRejected)
	fmt.Fprintf(w, "checkpoint_resumes=%d\n", s.CheckpointResumes)
	fmt.Fprintf(w, "checkpoint_commits=%d\n", s.CheckpointCommits)
	fmt.Fprintf(w, "keyframes=%d\n", s.Keyframes)
	fmt.Fprintf(w, "groups=%d missing_groups=%d\n", s.Groups, s.MissingGroups)
	fmt.Fprintf(w, "layout_issues=%d\n", s.LayoutIssues)
	for i, h := range s.SegmentHashes {
		fmt.Fprintf(w, "segment_hashes[%d]=%s\n", i+1, h)
	}
}
