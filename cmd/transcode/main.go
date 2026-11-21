// Command transcode is the offline transcoding batch processor.
//
// Usage:
//
//	transcode run  --input in.mp4 --target-kbps 1800 --segment 3s --workers 2 --out ./artifacts
//	transcode demo --duration 12s --target-kbps 1800 --segment 3s --workers 2 --out ./artifacts
//	transcode resume --state ./artifacts/state.json
//
// Exit codes: 0 success, 1 partial failure or interruption, 2 bad
// parameters or input.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"transcode/internal/config"
	"transcode/internal/job"
	"transcode/internal/media"
	"transcode/internal/rate"
	"transcode/internal/report"
	"transcode/internal/scheduler"
	"transcode/internal/segment"
)

func main() { os.Exit(run(os.Args)) }

func run(args []string) int {
	if len(args) < 2 {
		usage()
		return 2
	}
	switch args[1] {
	case "run", "demo":
		cfg, demoDur, code := parseRunFlags(args[1], args[2:])
		if code != 0 {
			return code
		}
		return execute(cfg, demoDur)
	case "resume":
		fs := flag.NewFlagSet("resume", flag.ContinueOnError)
		statePath := fs.String("state", "", "path to state.json of an interrupted run")
		if fs.Parse(args[2:]) != nil || *statePath == "" {
			fmt.Fprintln(os.Stderr, "resume requires --state")
			return 2
		}
		st, err := job.Load(*statePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "load state:", err)
			return 2
		}
		var cfg config.Config
		if len(st.Config) == 0 {
			fmt.Fprintln(os.Stderr, "state file carries no config; cannot resume")
			return 2
		}
		if err := json.Unmarshal(st.Config, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, "parse stored config:", err)
			return 2
		}
		cfg.StatePath = *statePath
		return execute(cfg, 0)
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: transcode <run|demo|resume> [flags]")
}

func parseRunFlags(cmd string, args []string) (config.Config, float64, int) {
	cfg := config.Defaults()
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	var segDur, demoDur time.Duration
	fs.StringVar(&cfg.Input, "input", "", "input video path (run mode)")
	fs.IntVar(&cfg.TargetKbps, "target-kbps", cfg.TargetKbps, "target total bitrate in kbps")
	fs.DurationVar(&segDur, "segment", 3*time.Second, "segment duration")
	fs.IntVar(&cfg.Workers, "workers", cfg.Workers, "max concurrent ffmpeg subprocesses")
	fs.StringVar(&cfg.OutDir, "out", "", "output directory")
	fs.StringVar(&cfg.ReportPath, "report", "", "report JSON path (default OUT/report.json)")
	fs.StringVar(&cfg.Codec, "codec", cfg.Codec, "ffmpeg encoder name")
	fs.IntVar(&cfg.MinSegKbps, "min-seg-kbps", cfg.MinSegKbps, "per-segment bitrate floor")
	fs.IntVar(&cfg.MaxSegKbps, "max-seg-kbps", cfg.MaxSegKbps, "per-segment bitrate cap")
	fs.IntVar(&cfg.MaxRetries, "max-retries", cfg.MaxRetries, "per-segment retry limit")
	fs.Float64Var(&cfg.PSNRFloorDB, "psnr-floor-db", cfg.PSNRFloorDB, "frame PSNR threshold")
	fs.DurationVar(&demoDur, "duration", 0, "demo mode: sample duration")
	if fs.Parse(args) != nil {
		return cfg, 0, 2
	}
	cfg.SegmentSec = segDur.Seconds()
	if cmd == "demo" {
		if demoDur <= 0 {
			fmt.Fprintln(os.Stderr, "demo requires --duration")
			return cfg, 0, 2
		}
	} else if cfg.Input == "" {
		fmt.Fprintln(os.Stderr, "run requires --input")
		return cfg, 0, 2
	}
	return cfg, demoDur.Seconds(), 0
}

// execute runs the full pipeline and returns the process exit code.
func execute(cfg config.Config, demoDur float64) int {
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if demoDur > 0 {
		sample := filepath.Join(cfg.OutDir, "sample.mp4")
		if _, err := os.Stat(sample); err != nil {
			fmt.Println("generating deterministic sample:", sample)
			if err := media.GenerateSample(ctx, sample, demoDur, 30); err != nil {
				fmt.Fprintln(os.Stderr, "generate sample:", err)
				return 2
			}
		}
		cfg.Input = sample
	}

	info, err := media.Probe(cfg.Input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		return 2
	}
	digest, err := media.Digest(cfg.Input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "digest:", err)
		return 2
	}
	segs, err := segment.Plan(info, cfg.SegmentSec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "segment plan failed:", err)
		return 2
	}
	fmt.Printf("planned %d segments over %.1fs (%s, %dx%d)\n",
		len(segs), info.DurationSec, info.CodecName, info.Width, info.Height)

	metrics, err := rate.Analyze(ctx, cfg.Input, info.AvgFPS, segs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pre-analysis:", err)
		return 1
	}
	ctrl := rate.New(float64(cfg.TargetKbps), segs, metrics,
		float64(cfg.MinSegKbps), float64(cfg.MaxSegKbps))

	hash, err := runHash(cfg, digest, segs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	st, reused, code := loadOrCreateState(cfg, hash, digest, segs)
	if code != 0 {
		return code
	}
	// Replay measured actuals so the budget accounting matches the
	// original run before allocating targets for pending segments.
	for i, s := range st.Segments {
		if s.Status == job.Succeeded {
			ctrl.Feedback(i, s.TargetKbps, s.ActualKbps)
		}
	}

	runner := scheduler.New(cfg, info, segs, st, ctrl)
	if err := runner.VerifyReusable(); err != nil {
		fmt.Fprintln(os.Stderr, "verify artifacts:", err)
		return 1
	}
	if err := runner.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
	}

	rep := report.Build(st, float64(cfg.TargetKbps), reused)
	if err := report.WriteJSON(rep, cfg.ReportPath); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		return 1
	}
	report.PrintSummary(os.Stdout, rep)
	switch {
	case len(rep.FailedSegments) > 0:
		return 1
	case !st.Done():
		fmt.Fprintln(os.Stderr, "interrupted: rerun the same command or `transcode resume --state", cfg.StatePath+"`")
		return 1
	}
	return 0
}

// runHash identifies a run by every parameter that affects results.
func runHash(cfg config.Config, digest string, segs []segment.Segment) (string, error) {
	id := struct {
		cfg   config.Config
		Digest string
		Bounds [][2]float64
	}{cfg, digest, nil}
	for _, s := range segs {
		id.Bounds = append(id.Bounds, [2]float64{s.StartSec, s.EndSec})
	}
	return job.HashConfig(id)
}

// loadOrCreateState resumes an existing manifest when the run identity
// matches, or starts a fresh one. A mismatch refuses to mix results.
func loadOrCreateState(cfg config.Config, hash, digest string, segs []segment.Segment) (*job.State, map[int]bool, int) {
	reused := map[int]bool{}
	st, err := job.Load(cfg.StatePath)
	if err == nil {
		if st.ConfigHash != hash || st.InputDigest != digest {
			fmt.Fprintln(os.Stderr, "state", cfg.StatePath,
				"was created with different input or parameters; refusing to mix results (use a fresh --out)")
			return nil, nil, 2
		}
		for _, s := range st.Segments {
			if s.Status == job.Succeeded {
				reused[s.Index] = true
			}
		}
		if len(reused) > 0 {
			fmt.Printf("resuming: %d/%d segments already complete\n", len(reused), len(st.Segments))
		}
		return st, reused, 0
	}
	segStates := make([]job.SegmentState, len(segs))
	for i, s := range segs {
		segStates[i] = job.SegmentState{Index: i, Status: job.Queued, StartSec: s.StartSec, EndSec: s.EndSec}
	}
	st = job.New(hash, digest, cfg.Input, segStates)
	cfgJSON, _ := json.Marshal(cfg)
	st.Config = cfgJSON
	if err := st.SaveAs(cfg.StatePath); err != nil {
		fmt.Fprintln(os.Stderr, "save state:", err)
		return nil, nil, 1
	}
	return st, reused, 0
}
