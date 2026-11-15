// Package app orchestrates the fixture -> ingest -> timeline -> segment ->
// manifest -> verify pipeline for the three hlsforge subcommands.
package app

import (
	"fmt"
	"os"
	"path/filepath"

	"hlsforge/internal/cli"
	"hlsforge/internal/fixture"
	"hlsforge/internal/ingest"
	"hlsforge/internal/manifest"
	"hlsforge/internal/report"
	"hlsforge/internal/segment"
	"hlsforge/internal/timeline"
	"hlsforge/internal/verify"
)

// RunDemo generates deterministic CMAF samples with optional fault injection.
func RunDemo(cfg *cli.DemoConfig) (*report.Report, error) {
	metas, err := fixture.Generate(cfg.Out, cfg.Bitrates, fixture.Options{
		Seed: cfg.Seed, PartMs: cfg.Part, DurationMs: cfg.Duration, Faults: cfg.Faults,
	})
	if err != nil {
		return nil, err
	}
	rep := &report.Report{Command: "demo"}
	for _, m := range metas {
		rep.Tracks = append(rep.Tracks, report.TrackSummary{
			BitrateKbps: m.Bitrate / 1000,
			Fragments:   len(m.Present),
			Parts:       len(m.Present),
			DurationS:   float64(cfg.Duration) / 1000.0,
			Failures:    len(m.Missing),
		})
		// Injected gaps are intentional fixture state; demo always exits 0.
		for _, seq := range m.Missing {
			rep.AddFinding(report.Finding{
				Code: report.CodeMissingPart, Bitrate: m.Bitrate, Seq: seq,
				Detail: "injected missing fragment (GAP slot retained)",
			})
		}
	}
	return rep, nil
}

// RunBuild ingests an inbox and writes the HLS output tree.
func RunBuild(cfg *cli.BuildConfig) (*report.Report, error) {
	collected, err := ingest.Collect(cfg.In, cfg.FromSeq, cfg.ToSeq, cfg.Part)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		return nil, err
	}
	rep := &report.Report{Command: "build"}
	rep.Findings = append(rep.Findings, collected.Findings...)
	var inputs []manifest.TrackInput
	for _, ct := range collected.Tracks {
		tr, err := timeline.Build(ct)
		if err != nil {
			return nil, err
		}
		layout, err := segment.Layout(tr, cfg.Segment, cfg.Part)
		if err != nil {
			return nil, err
		}
		if _, err := segment.Persist(cfg.Out, tr, layout); err != nil {
			return nil, err
		}
		if err := copyInit(ct, cfg.Out, layout.SegmentDir); err != nil {
			return nil, err
		}
		inputs = append(inputs, manifest.TrackInput{
			Bitrate: ct.Bitrate, Layout: layout,
			InitURI: filepath.Join(layout.SegmentDir, fixture.InitName()),
			HasPreload: true, CanSkipTo: true,
		})
		rep.Tracks = append(rep.Tracks, report.TrackSummary{
			BitrateKbps: ct.Bitrate / 1000,
			Fragments:   len(ct.Present()),
			Segments:    len(layout.Segments),
			Parts:       layout.PartCount(),
			DurationS:   float64(tr.EndMs) / 1000.0,
			Failures:    len(ct.Missing()),
		})
	}
	if err := manifest.WriteAll(cfg.Out, "master.m3u8", inputs); err != nil {
		return nil, err
	}
	return rep, nil
}

// RunVerify re-reads a build and reports consistency failures.
func RunVerify(cfg *cli.VerifyConfig) (*report.Report, error) {
	summaries, findings, err := verify.Verify(verify.Input{
		Inbox: cfg.In, HLS: cfg.HLS, FromSeq: cfg.FromSeq, ToSeq: cfg.ToSeq,
		PartMs: cfg.Part, SegmentMs: cfg.Segment, ToleranceMs: cfg.Tolerance,
	})
	if err != nil {
		return nil, err
	}
	rep := &report.Report{Command: "verify", Tracks: summaries, Findings: findings}
	rep.MarkConsistency()
	return rep, nil
}

// copyInit places a track's init segment under the HLS output for EXT-X-MAP.
func copyInit(ct *ingest.TrackIngest, out, segDir string) error {
	src := filepath.Join(ct.TrackDir(), fixture.InitName())
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("app: read init: %w", err)
	}
	dstDir := filepath.Join(out, segDir)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dstDir, fixture.InitName()), raw, 0o644)
}
