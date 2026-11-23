// Command mediaverify runs the deterministic broadcast re-packaging
// verification pipeline from locally generated sample data.
//
// Exit codes: 0 completed with all invariants passing; 2 corrupt input or
// illegal parameters; 1 internal error or cross-stage invariant failure.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
	"mediaverify/internal/fixture"
	"mediaverify/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediaverify demo [flags]")
		return 2
	}
	cfg, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "parameter error: %v\n", err)
		return 2
	}
	result, err := pipeline.Run(cfg)
	// A run that reaches the report stage still prints its findings even if
	// an invariant subsequently fails, so operators can see why.
	printReport(result, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline error: %v\n", err)
		switch errs.Class(err) {
		case errs.KindInvalidInput, errs.KindLimitExceeded, errs.KindChecksum, errs.KindTimestamp:
			return 2
		default:
			return 1
		}
	}
	fmt.Println("status=ok invariants=passed")
	return 0
}

func parseFlags(flags []string) (pipeline.Config, error) {
	cfg := pipeline.Config{
		Seed:       1,
		Duration:   clock.Nanoseconds(30_000_000_000),
		Segment:    clock.Nanoseconds(2_000_000_000),
		Ladder:     []int64{400_000, 900_000, 1_800_000},
		LateWindow: clock.Nanoseconds(750_000_000),
		Fault:      fixture.FaultNone,
		FrameRate:  clock.FrameRate25(),
		OutPath:    "manifest.json",
	}
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		val := func() (string, error) {
			if i+1 >= len(flags) {
				return "", fmt.Errorf("flag %s needs a value", f)
			}
			i++
			return flags[i], nil
		}
		switch f {
		case "--seed":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return cfg, fmt.Errorf("--seed: %v", err)
			}
			cfg.Seed = n
		case "--duration":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return cfg, fmt.Errorf("--duration: %v", err)
			}
			cfg.Duration = clock.Nanoseconds(d.Nanoseconds())
		case "--segment":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return cfg, fmt.Errorf("--segment: %v", err)
			}
			cfg.Segment = clock.Nanoseconds(d.Nanoseconds())
		case "--late-window":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return cfg, fmt.Errorf("--late-window: %v", err)
			}
			cfg.LateWindow = clock.Nanoseconds(d.Nanoseconds())
		case "--ladder":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			rungs, err := parseLadder(v)
			if err != nil {
				return cfg, err
			}
			cfg.Ladder = rungs
		case "--fault":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			cfg.Fault = v
		case "--fps":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			fr, err := parseFPS(v)
			if err != nil {
				return cfg, err
			}
			cfg.FrameRate = fr
		case "--out":
			v, err := val()
			if err != nil {
				return cfg, err
			}
			cfg.OutPath = v
		default:
			return cfg, fmt.Errorf("unknown flag %q", f)
		}
	}
	return cfg, nil
}

func parseLadder(s string) ([]int64, error) {
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid ladder rung %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func parseFPS(s string) (clock.FrameRate, error) {
	drop := strings.HasSuffix(s, "df")
	s = strings.TrimSuffix(strings.TrimSuffix(s, "df"), "ndf")
	switch strings.TrimSpace(s) {
	case "24":
		return clock.FrameRate24(), nil
	case "25":
		return clock.FrameRate25(), nil
	case "30":
		return clock.FrameRate30(), nil
	case "50":
		return clock.FrameRate50(), nil
	case "29.97":
		return clock.FrameRate2997(drop), nil
	case "59.94":
		return clock.FrameRate5994(drop), nil
	default:
		return clock.FrameRate{}, fmt.Errorf("unsupported fps %q", s)
	}
}

func printReport(r pipeline.Result, cfg pipeline.Config) {
	rep := r.Report
	fmt.Printf("target_bps=%d actual_bps=%d bitrate_error_pct=%.2f\n",
		rep.TargetBPS, rep.ActualBPS, rep.BitrateErrorPct)
	fmt.Printf("packets_duplicate=%d packets_reordered=%d packets_lost=%d\n",
		r.Duplicate, r.Reordered, r.Lost)
	fmt.Printf("segments=%d alignment_max_frames=%d subtitle_spans=%d\n",
		rep.Segments, rep.AlignmentMaxFrame, r.SubtitleSpans)
	fmt.Printf("recovered_segments=%d degraded_intervals=%d replay_count=%d\n",
		rep.RecoveredSegments, rep.DegradedIntervals, r.ReplayCount)
	if r.ManifestHash != "" {
		fmt.Printf("manifest_sha256=%s\n", r.ManifestHash)
	}
	if r.ManifestPath != "" {
		fmt.Printf("manifest_path=%s\n", r.ManifestPath)
	}
	_ = cfg
}
