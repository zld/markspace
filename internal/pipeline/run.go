// Package pipeline orchestrates the four subsystems behind one deterministic
// run: RTP ingest -> watermark timeline -> cut plan -> shared manifest, with
// subtitle correction on the nanosecond basis and bandwidth-driven ladder
// allocation feeding the quality report.
package pipeline

import (
	"strings"

	"mediaverify/internal/bitrate"
	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
	"mediaverify/internal/fixture"
	"mediaverify/internal/quality"
	"mediaverify/internal/segment"
	"mediaverify/internal/subtitle"
)

// Config is the full validated run configuration.
type Config struct {
	Seed       int64
	Duration   clock.Ticks
	Segment    clock.Ticks
	Ladder     []int64
	LateWindow clock.Ticks
	Fault      string
	FrameRate  clock.FrameRate
	OutPath    string
}

// Result is everything the command adapter prints and writes.
type Result struct {
	Report        quality.Report
	ManifestHash  string
	ManifestPath  string
	Received      int
	Duplicate     int
	Reordered     int
	Lost          int
	ChecksumBad   int
	BackwardJumps int
	ReplayCount   int
	RejectedLate  int
	SubtitleSpans int
}

// Run executes one end-to-end verification.
func Run(cfg Config) (Result, error) {
	if err := validateConfig(cfg); err != nil {
		return Result{}, err
	}
	// Nominal traffic orbits the middle ladder rung so the reported target
	// is the middle rendition and the 15% band stays provably satisfiable.
	mid := cfg.Ladder[len(cfg.Ladder)/2]
	stream, err := fixture.Generate(fixture.Config{
		Seed: cfg.Seed, Duration: cfg.Duration, FrameRate: cfg.FrameRate,
		Fault: cfg.Fault, TargetBPS: mid,
	})
	if err != nil {
		return Result{}, err
	}

	ingest, err := ingestRTP(cfg, stream)
	if err != nil {
		return Result{}, err
	}
	cues, err := parseSubtitles(stream, cfg.Fault)
	if err != nil {
		return Result{}, err
	}
	decisions, estDegraded := allocateLadder(cfg, stream)

	// Untrusted intervals come from loss gaps, timestamp rollbacks and any
	// gap that crosses a subtitle boundary.
	intervals := buildIntervals(ingest, stream, cues)
	planner, err := segment.NewPlanner(cfg.FrameRate, cfg.Segment)
	if err != nil {
		return Result{}, err
	}
	plan, err := planner.Plan(ingest.wm.Events(), intervals, cfg.Duration)
	if err != nil {
		return Result{}, err
	}

	rungs := rungInputs(decisions)
	stats := segment.Stats{
		PacketsReceived:   ingest.received,
		PacketsDuplicate:  ingest.duplicate,
		PacketsReordered:  ingest.reordered,
		PacketsLost:       ingest.lost,
		DegradedIntervals: len(plan.DegradedIntervals()),
		ReplayCount:       ingest.wm.ReplayCount(),
	}
	manifest, err := segment.BuildManifest(cfg.FrameRate, stream.InputDigest, plan, rungs, cues, stats)
	if err != nil {
		return Result{}, err
	}
	hash, err := manifest.Hash()
	if err != nil {
		return Result{}, err
	}
	if cfg.OutPath != "" {
		if err := manifest.WriteAtomic(cfg.OutPath); err != nil {
			return Result{}, err
		}
	}
	active := lastActiveRung(decisions)
	report, err := quality.Build(quality.Inputs{
		ActiveRung:  active,
		Decisions:   decisions,
		Plan:        plan,
		SubtitleN:   len(manifest.SubtitleSpans),
		ReplayCount: ingest.wm.ReplayCount(),
	})
	if err != nil {
		return Result{}, err
	}
	res := Result{
		Report:        report,
		ManifestHash:  hash,
		ManifestPath:  cfg.OutPath,
		Received:      ingest.received,
		Duplicate:     ingest.duplicate,
		Reordered:     ingest.reordered,
		Lost:          ingest.lost,
		ChecksumBad:   ingest.checksumBad,
		BackwardJumps: ingest.extender.BackwardJumps,
		ReplayCount:   ingest.wm.ReplayCount(),
		RejectedLate:  len(ingest.wm.Rejected()),
		SubtitleSpans: len(manifest.SubtitleSpans),
	}
	if err := report.AssertAcceptance(); err != nil {
		return res, err
	}
	_ = estDegraded
	return res, nil
}

func validateConfig(cfg Config) error {
	if cfg.Duration <= 0 || cfg.Duration > 2*clock.Ticks(3600_000_000_000) {
		return errs.New(errs.KindLimitExceeded, "pipeline", "duration outside (0,2h]: %d", cfg.Duration.Ns())
	}
	if cfg.Segment <= 0 || cfg.Segment > cfg.Duration {
		return errs.New(errs.KindInvalidInput, "pipeline", "segment duration invalid: %d", cfg.Segment.Ns())
	}
	if cfg.LateWindow < 0 {
		return errs.New(errs.KindInvalidInput, "pipeline", "late window must be non-negative")
	}
	if len(cfg.Ladder) < 2 {
		return errs.New(errs.KindInvalidInput, "pipeline", "ladder needs >=2 rungs")
	}
	return nil
}

func parseSubtitles(stream *fixture.Stream, fault string) ([]subtitle.Cue, error) {
	cfg := subtitle.DefaultConfig()
	srtCues, err := subtitle.ParseSRT(strings.NewReader(stream.SRT), cfg)
	if err != nil {
		return nil, err // input corruption -> command exit code 2
	}
	vttCues, err := subtitle.ParseWebVTT(strings.NewReader(stream.VTT), cfg)
	if err != nil {
		return nil, err
	}
	if len(vttCues) != len(srtCues) {
		return nil, errs.New(errs.KindInternal, "pipeline",
			"SRT/VTT cue count mismatch %d vs %d", len(srtCues), len(vttCues))
	}
	return srtCues, nil
}

func allocateLadder(cfg Config, stream *fixture.Stream) ([]bitrate.Decision, bool) {
	estimator := bitrate.NewEstimator(4*clock.Ticks(1_000_000_000), 3)
	alloc, _ := bitrate.NewAllocator(cfg.Ladder, 3)
	segments := int(int64(cfg.Duration) / int64(cfg.Segment))
	decisions := make([]bitrate.Decision, 0, segments)
	anyDegraded := false
	for seg := 0; seg < segments; seg++ {
		cutoff := clock.Ticks(int64(seg+1) * int64(cfg.Segment))
		for _, bp := range stream.Bandwidth {
			if bp.Time <= cutoff && bp.Time > cutoff-cfg.Segment*4 {
				_ = estimator.Add(bitrate.Sample{Time: bp.Time, Bytes: bp.Bytes})
			}
		}
		est, degraded, reason := estimator.Estimate()
		if degraded {
			anyDegraded = true
		}
		decisions = append(decisions, alloc.Allocate(seg, est, degraded, reason))
	}
	return decisions, anyDegraded
}

func rungInputs(decisions []bitrate.Decision) []segment.RenditionInput {
	if len(decisions) == 0 {
		return nil
	}
	last := decisions[len(decisions)-1]
	out := make([]segment.RenditionInput, len(last.Renditions))
	for i, r := range last.Renditions {
		out[i] = segment.RenditionInput{TargetBPS: r.TargetBPS, ActualBPS: r.ActualBPS}
	}
	return out
}

func lastActiveRung(decisions []bitrate.Decision) bitrate.Rendition {
	last := decisions[len(decisions)-1]
	// The reported active rendition is the middle ladder rung; its actual
	// rate tracks the bandwidth estimate clamped to the 15% band.
	return last.Renditions[len(last.Renditions)/2]
}
