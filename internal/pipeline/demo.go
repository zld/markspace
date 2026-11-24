package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"archive/internal/clock"
	"archive/internal/manifest"
	"archive/internal/normalize"
	"archive/internal/probe"
	"archive/internal/rtp"
	"archive/internal/timeline"
)

// Run executes the demo end to end.
func Run(opts Options) (*Result, error) {
	if opts.OutDir == "" {
		return nil, clock.New(clock.KindInvalidMetadata, "output directory is required")
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, clock.New(clock.KindInvalidMetadata, "create output directory").WithCause(err)
	}
	sum := &Summary{
		Command:           "demo",
		OutputDir:         opts.OutDir,
		IsolatedLayers:    []string{},
		RecoveredLayers:   []string{},
		BandwidthSchedule: []string{},
	}
	assets, err := ensureAssets(opts)
	if err != nil {
		return nil, err
	}
	sum.NormalSample = assets.NormalMP4
	sum.Boundary = assets.BoundaryClip
	sum.Corrupt = assets.CorruptInfo

	var compTracks []*probe.Track
	var resolutions []*normalize.Resolution
	for _, path := range []string{assets.NormalMP4, assets.NormalMXF} {
		c, err := probeFile(path)
		if err != nil {
			return nil, err
		}
		for _, t := range c.Tracks {
			res, err := normalize.Resolve(t)
			if err != nil {
				return nil, err
			}
			sum.Warnings = append(sum.Warnings, res.Warnings...)
			resolutions = append(resolutions, res)
			compTracks = append(compTracks, t)
		}
	}
	composed, err := timeline.Compose(compTracks, resolutions)
	if err != nil {
		return nil, err
	}
	for _, s := range composed.Skipped {
		sum.Warnings = append(sum.Warnings, fmt.Sprintf("skipped track %s: %v", s.TrackID, s.Reason))
	}

	frames, pngs, err := renderTracks(opts, composed.Timeline)
	if err != nil {
		return nil, err
	}

	refTrack := viewOf(composed.Timeline.Tracks[0])
	frameCount := composed.Timeline.Tracks[0].FrameCount()
	schedule, err := bandwidthPlan(opts.Bandwidth)
	if err != nil {
		return nil, err
	}
	sum.BandwidthSchedule = schedule

	normal, err := runScenario("normal", refTrack, frameCount, modeClean, opts.LossBurst)
	if err != nil {
		return nil, err
	}
	burst, err := runScenario("burst", refTrack, frameCount, modeBurst, opts.LossBurst)
	if err != nil {
		return nil, err
	}
	sum.Normal = normal
	sum.Burst = burst

	sum.TargetBitrateKbps = normal.TargetBitrateKbps
	sum.ActualBitrateKbps = normal.ActualBitrateKbps
	sum.BitrateDeviationPct = normal.BitrateDeviationPct
	sum.QualityPSNRDb = normal.QualityPSNRDb
	sum.ManifestAlignmentNS = normal.ManifestAlignNS
	sum.Duplicates = normal.Duplicates
	sum.Reordered = normal.Reordered
	sum.Lost = normal.Lost
	sum.RecoveredLayers = normal.RecoveredLayers
	sum.IsolatedLayers = normal.IsolatedLayers
	for _, l := range normal.Log {
		sum.RecoveryLog = append(sum.RecoveryLog, "normal: "+l)
	}
	for _, l := range burst.Log {
		sum.RecoveryLog = append(sum.RecoveryLog, "burst: "+l)
	}

	boundaryNS, boundaryErr := probeBoundary(assets)
	if boundaryErr != nil {
		return nil, boundaryErr
	}
	sum.BoundaryErrorNS = boundaryNS

	corruptErr := probeCorrupt(assets.CorruptMP4)
	sum.CorruptError = corruptErr.Error()
	invalidErr := probeInvalid(assets.InvalidMP4)
	if invalidErr == nil {
		return nil, clock.New(clock.KindInvalidMetadata, "illegal crop sample normalised without error")
	}
	sum.InvalidMetadata = InvalidInfo{
		File:     assets.InvalidMP4,
		Error:    invalidErr.Error(),
		Kind:     string(clock.AsKind(invalidErr)),
		ExitCode: ClassifyExit(invalidErr),
	}
	sum.Corrupt.ExitCode = ClassifyExit(corruptErr)
	sum.ExitCode = 0
	if burst.ActiveLayers < 1 {
		sum.ExitCode = ClassifyExit(clock.New(clock.KindMisaligned, "no representation layer recovered"))
	}

	// Build and write the manifest from the frames plus the normal layer view.
	layerEntries := layerManifestEntries(normal)
	mf := manifest.Build(composed.Timeline.DurationNS, sum.ManifestAlignmentNS, frames, layerEntries, sum.Warnings)
	if err := mf.WriteFrames(filepath.Join(opts.OutDir, "frames"), pngs); err != nil {
		return nil, err
	}
	if err := mf.WriteJSON(filepath.Join(opts.OutDir, "manifest.json")); err != nil {
		return nil, err
	}
	sum.FrameCount = mf.FrameCount()
	sum.FrameSHA256 = mf.AggregateHash()
	mj, err := mf.JSON()
	if err != nil {
		return nil, err
	}
	sum.ManifestSHA256 = manifest.HashSHA256(mj)

	if err := writeSummary(filepath.Join(opts.OutDir, "summary.json"), sum); err != nil {
		return nil, err
	}
	res := &Result{Summary: sum, ExitCode: sum.ExitCode}
	if sum.ExitCode != 0 {
		res.Err = clock.New(clock.KindMisaligned, "no usable representation layer remained")
	}
	return res, nil
}

func probeFile(path string) (*probe.Container, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, clock.New(clock.KindInvalidMetadata, "read sample").WithLocation(path).WithCause(err)
	}
	return probe.Sniff(data)
}

func probeCorrupt(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return clock.New(clock.KindInvalidMetadata, "read corrupt sample").WithLocation(path).WithCause(err)
	}
	if _, err := probe.Sniff(data); err != nil {
		return err
	}
	return clock.New(clock.KindInvalidMetadata, "corrupt sample parsed without error").WithLocation(path)
}

func bandwidthPlan(samples []int) ([]string, error) {
	geoms := defaultGeometries()
	layers := make([]rtp.Layer, 0, len(geoms))
	for _, g := range geoms {
		layers = append(layers, rtp.Layer{Name: g.name, TargetKbps: g.kbps})
	}
	sched := rtp.Scheduler{Samples: samples}
	return sched.Plan(layers)
}

func boundaryError(spec BoundarySpec, tl *timeline.Timeline) int64 {
	var worst int64
	for _, tr := range tl.Tracks {
		if d := timeline.BoundaryError(spec.StartNS, tr.FrameIntervalNS); d > worst {
			worst = d
		}
	}
	return worst
}

func writeSummary(path string, sum *Summary) error {
	b, err := jsonMarshal(sum)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// probeBoundary parses the generated boundary sample and confirms its single
// track's frame grid lands exactly on the clip boundary declared in
// BoundarySpec. An off-by-anything is a reported error, not a rounded patch.
func probeBoundary(assets *AssetPaths) (int64, error) {
	c, err := probeFile(assets.BoundaryMP4)
	if err != nil {
		return 0, err
	}
	if len(c.Tracks) != 1 {
		return 0, clock.Newf(clock.KindInvalidMetadata, "boundary sample has %d tracks, want 1", len(c.Tracks))
	}
	t := c.Tracks[0]
	if t.SampleIntervalNS != assets.BoundaryClip.FrameIntervalNS {
		return 0, clock.Newf(clock.KindMisaligned,
			"boundary sample interval %d ns differs from declared %d ns",
			t.SampleIntervalNS, assets.BoundaryClip.FrameIntervalNS)
	}
	var worst int64
	for _, b := range []int64{assets.BoundaryClip.StartNS, assets.BoundaryClip.EndNS} {
		if d := timeline.BoundaryError(b, t.SampleIntervalNS); d > worst {
			worst = d
		}
	}
	if worst > 1 {
		return worst, clock.Newf(clock.KindMisaligned, "boundary error %d ns exceeds 1 ns", worst)
	}
	return worst, nil
}

// ensureAssets reuses an existing deterministic asset directory unless the
// caller asked for regeneration or a file is missing.
func ensureAssets(opts Options) (*AssetPaths, error) {
	dir := filepath.Join(opts.OutDir, "assets")
	if !opts.Generate {
		paths, err := Generate(dir)
		if err == nil && allPresent(paths) {
			return paths, nil
		}
	}
	return Generate(dir)
}

func allPresent(p *AssetPaths) bool {
	for _, f := range []string{p.NormalMP4, p.NormalMXF, p.BoundaryMP4, p.CorruptMP4, p.InvalidMP4} {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}
	return true
}

// probeInvalid parses and normalises a sample expected to violate a rule.
func probeInvalid(path string) error {
	c, err := probeFile(path)
	if err != nil {
		return err
	}
	for _, t := range c.Tracks {
		if _, err := normalize.Resolve(t); err != nil {
			return err
		}
	}
	return nil
}
