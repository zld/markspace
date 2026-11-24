package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"archive/internal/clock"
	"archive/internal/manifest"
	"archive/internal/normalize"
	"archive/internal/probe"
	"archive/internal/render"
	"archive/internal/rtp"
	"archive/internal/timeline"
)

// Options controls one demo run.
type Options struct {
	OutDir     string
	Generate   bool
	LossBurst  int
	Bandwidth  []int
	FrameLimit int
}

// DefaultOptions returns the acceptance-command defaults.
func DefaultOptions() Options {
	return Options{
		LossBurst: 3,
		Bandwidth: []int{900, 420, 1100},
	}
}

// Scenario summarises one transport pass.
type Scenario struct {
	Mode                string      `json:"mode"`
	LayerCount          int         `json:"layer_count"`
	ActiveLayers        int         `json:"active_layers"`
	TargetBitrateKbps   int         `json:"target_bitrate_kbps"`
	ActualBitrateKbps   float64     `json:"actual_bitrate_kbps"`
	BitrateDeviationPct float64     `json:"bitrate_deviation_pct"`
	QualityPSNRDb       float64     `json:"quality_psnr_db"`
	Duplicates          int         `json:"duplicates"`
	Reordered           int         `json:"reordered"`
	Lost                int         `json:"lost"`
	Gaps                int         `json:"gaps"`
	ManifestAlignNS     int64       `json:"manifest_alignment_ns"`
	RecoveredLayers     []string    `json:"recovered_layers"`
	IsolatedLayers      []string    `json:"isolated_layers"`
	Layers              []LayerStat `json:"layers"`
	Log                 []string    `json:"recovery_log"`
}

// Summary is the JSON document printed on stdout.
type Summary struct {
	Command             string         `json:"command"`
	OutputDir           string         `json:"output_dir"`
	NormalSample        string         `json:"normal_sample"`
	Normal              Scenario       `json:"normal"`
	Burst               Scenario       `json:"burst"`
	TargetBitrateKbps   int            `json:"target_bitrate_kbps"`
	ActualBitrateKbps   float64        `json:"actual_bitrate_kbps"`
	BitrateDeviationPct float64        `json:"bitrate_deviation_pct"`
	QualityPSNRDb       float64        `json:"quality_psnr_db"`
	FrameCount          int            `json:"frame_count"`
	FrameSHA256         string         `json:"frame_sha256"`
	ManifestSHA256      string         `json:"manifest_sha256"`
	ManifestAlignmentNS int64          `json:"manifest_alignment_ns"`
	Duplicates          int            `json:"duplicates"`
	Reordered           int            `json:"reordered"`
	Lost                int            `json:"lost"`
	IsolatedLayers      []string       `json:"isolated_layers"`
	RecoveredLayers     []string       `json:"recovered_layers"`
	BandwidthSchedule   []string       `json:"bandwidth_schedule"`
	Boundary            BoundarySpec   `json:"boundary"`
	BoundaryErrorNS     int64          `json:"boundary_error_ns"`
	Corrupt             TruncationInfo `json:"corrupt_sample"`
	CorruptError        string         `json:"corrupt_error"`
	RecoveryLog         []string       `json:"recovery_log"`
	Warnings            []string       `json:"warnings,omitempty"`
	ExitCode            int            `json:"exit_code"`
}

// Result carries the summary and the process exit code. Err is set when the
// run must fail; recovery success keeps the exit code at zero while the
// deliberately corrupt sample's non-zero code stays in the summary.
type Result struct {
	Summary  *Summary
	ExitCode int
	Err      error
}

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
	assets, err := Generate(filepath.Join(opts.OutDir, "assets"))
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

	refTrack := composed.Timeline.Tracks[0]
	frameCount := refTrack.FrameCount()
	schedule, err := bandwidthPlan(opts.Bandwidth)
	if err != nil {
		return nil, err
	}
	sum.BandwidthSchedule = schedule

	normal, err := runScenario("normal", composed.Timeline, refTrack, frameCount, modeClean, opts.LossBurst)
	if err != nil {
		return nil, err
	}
	burst, err := runScenario("burst", composed.Timeline, refTrack, frameCount, modeBurst, opts.LossBurst)
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

func renderTracks(opts Options, tl *timeline.Timeline) ([]manifest.FrameEntry, map[string][]byte, error) {
	var frames []manifest.FrameEntry
	pngs := make(map[string][]byte)
	for _, tr := range tl.Tracks {
		res := tr.Resolution
		limit := opts.FrameLimit
		if limit <= 0 {
			limit = tr.FrameCount()
		}
		idx := 0
		for _, clip := range tr.Clips {
			for _, ts := range timeline.FrameTimes(tr, clip) {
				if idx >= limit {
					break
				}
				ref, err := render.Reference(res.StoredWidth, res.StoredHeight, render.Seed(tr.ID), idx)
				if err != nil {
					return nil, nil, err
				}
				out, err := render.ApplyTransform(ref, res)
				if err != nil {
					return nil, nil, err
				}
				png, err := render.EncodePNG(out)
				if err != nil {
					return nil, nil, err
				}
				name := manifest.FrameFileName(tr.ID, idx)
				pngs[name] = png
				frames = append(frames, manifest.FrameEntry{
					TrackID:       tr.ID,
					TimestampNS:   ts,
					FrameIndex:    idx,
					Width:         out.Bounds().Dx(),
					Height:        out.Bounds().Dy(),
					Crop:          render.Crop(res),
					Rotation:      res.Rotation,
					ColorResolved: res.ColorResolved,
					File:          name,
				})
				idx++
			}
		}
	}
	if len(frames) == 0 {
		return nil, nil, clock.New(clock.KindInvalidMetadata, "no frames rendered")
	}
	return frames, pngs, nil
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
