// Package pipeline executes the kernel's stages in dependency order and keeps
// a record of every stage outcome, including recoverable ones. A run is pure
// with respect to its inputs: identical bytes always produce identical
// manifests.
package pipeline

import (
	"os"

	"mediacore/internal/audio"
	"mediacore/internal/container"
	"mediacore/internal/core"
	"mediacore/internal/graph"
	"mediacore/internal/manifest"
	"mediacore/internal/timeline"
)

// Options is a complete run request. Validation happens in the graph stage so
// every refusal carries a node location.
type Options struct {
	File         string
	SampleRate   int
	Channels     string
	TargetKbps   int
	LoudnessLUFS float64
	PeakDBFS     float64
	PacketMs     int
	OutPath      string
	Limits       container.Limits
	// ForceRescan rebuilds the index from verified records even when tables
	// are complete, which lets tests prove both paths agree.
	ForceRescan bool
}

// StageRecord is the outcome of one stage. Duration work is measured in
// samples and ticks, never wall-clock, so records are reproducible.
type StageRecord struct {
	Stage        string   `json:"stage"`
	OK           bool     `json:"ok"`
	IndexSource  string   `json:"index_source,omitempty"`
	Samples      int      `json:"samples,omitempty"`
	ValidSamples int      `json:"valid_samples,omitempty"`
	Gaps         int      `json:"gaps,omitempty"`
	Packets      int      `json:"packets,omitempty"`
	Recoverable  bool     `json:"recoverable,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

// Result is what the CLI prints and what tests assert on.
type Result struct {
	Manifest *manifest.Manifest
	Stages   []StageRecord
	Gaps     []container.Gap
	Recovery *container.TrackRecovery
	Graph    []graph.NodeID
	ExitCode int
	Err      error
}

// Run executes every stage. A returned Result is always non-nil.
func Run(opt Options) *Result {
	res := &Result{}
	if opt.Limits.MaxFileBytes == 0 {
		opt.Limits = container.DefaultLimits()
	}
	m, err := runStages(opt, res)
	if err != nil {
		res.Err = err
		res.ExitCode = core.ExitCode(err)
		return res
	}
	res.Manifest = m
	res.ExitCode = 0
	return res
}

func runStages(opt Options, res *Result) (*manifest.Manifest, error) {
	if opt.PeakDBFS == 0 {
		opt.PeakDBFS = -1
	}
	if opt.LoudnessLUFS == 0 {
		opt.LoudnessLUFS = -16
	}
	if opt.PacketMs == 0 {
		opt.PacketMs = 20
	}

	// Stage: scan.
	scan, err := container.ScanFile(opt.File, opt.Limits)
	if err != nil {
		return nil, err
	}
	rec := StageRecord{Stage: string(core.StageScan), OK: true, Samples: scan.BoxCount}
	for _, w := range scan.Warnings {
		rec.Errors = append(rec.Errors, w.Error())
		if !w.Recoverable {
			rec.Recoverable = false
		}
	}
	res.Stages = append(res.Stages, rec)

	var track *container.TrackScan
	for i := range scan.Tracks {
		if scan.Tracks[i].IsAudio {
			track = &scan.Tracks[i]
			break
		}
	}
	// moov loss is not fatal by itself: recovery can rebuild the index from
	// verifiable records. The graph still requires a track, so plan for one
	// when the scan found only lost-index damage.
	haveTrack := track != nil || hasRecoverableMdat(scan)

	// Stage: graph validation happens before recovery so a missing track or a
	// cycle is reported without reading media.
	var deps []graph.TrackDependency
	for _, t := range scan.Tracks {
		for _, d := range t.DependsOn {
			deps = append(deps, graph.TrackDependency{Track: t.ID, DependsOn: d})
		}
	}
	plan := graph.Plan{
		HaveAudioTrack: haveTrack,
		TrackDeps:      deps,
		SampleRate:     opt.SampleRate,
		Channels:       opt.Channels,
		TargetKbps:     opt.TargetKbps,
		LoudnessLUFS:   opt.LoudnessLUFS,
		PeakDBFS:       opt.PeakDBFS,
	}
	g, gerr := graph.Build(plan)
	if gerr != nil {
		res.Stages = append(res.Stages, StageRecord{
			Stage: string(core.StageGraph), OK: false,
			Errors: []string{gerr.Error()}, Recoverable: false,
		})
		return nil, gerr
	}
	res.Graph = g.Order()
	res.Stages = append(res.Stages, StageRecord{Stage: string(core.StageGraph), OK: true})

	// Stage: recover.
	recovery := container.Recovery{File: opt.File, Scan: scan, Limits: opt.Limits,
		ForceRescan: opt.ForceRescan}
	tr, rerr := recovery.Recover(track)
	if tr != nil {
		res.Recovery = tr
		res.Gaps = tr.Gaps
	}
	rrec := StageRecord{Stage: string(core.StageRecover), OK: rerr == nil,
		ValidSamples: countValid(tr), Gaps: len(gapsOf(tr))}
	if tr != nil {
		rrec.IndexSource = tr.IndexSource
		rrec.Samples = len(tr.Samples)
	}
	if rerr != nil {
		rrec.Errors = append(rrec.Errors, rerr.Error())
	}
	if tr != nil {
		for _, w := range tr.Warnings {
			rrec.Errors = append(rrec.Errors, w.Error())
		}
	}
	res.Stages = append(res.Stages, rrec)
	if rerr != nil {
		return nil, rerr
	}

	// Stage: audio. Layout, rate, and gain all come from the validated plan.
	outLayout, err := audio.ParseLayout(opt.Channels)
	if err != nil {
		return nil, core.Wrap(core.CodeParamConflict, core.StageAudio, false, "unsupported output layout", err)
	}
	inChannels := tr.Format.Channels
	if inChannels == 0 {
		inChannels = tr.SourceFormat.Channels
	}
	inLayout, err := audio.LayoutForChannels(inChannels)
	if err != nil {
		return nil, core.Wrap(core.CodeUnrecoverable, core.StageAudio, false,
			"source channel order is not defined", err)
	}
	segs, decErr := decodeSegments(opt.File, tr, inLayout)
	if decErr != nil {
		res.Stages = append(res.Stages, StageRecord{Stage: string(core.StageAudio), OK: false,
			Errors: []string{decErr.Error()}})
		return nil, decErr
	}
	inRate := tr.Format.SampleRate
	if inRate <= 0 {
		inRate = tr.SourceFormat.SampleRate
	}
	if inRate <= 0 {
		return nil, core.New(core.CodeUnrecoverable, core.StageAudio, false,
			"source sample rate is unknown")
	}
	processor := audio.SegmentProcessor{
		InRate:    inRate,
		OutRate:   opt.SampleRate,
		InLayout:  inLayout,
		OutLayout: outLayout,
		Gain:      audio.GainRequest{TargetLUFS: opt.LoudnessLUFS, PeakDBFS: opt.PeakDBFS},
	}
	processed, perr := processor.Process(segs)
	if perr != nil {
		res.Stages = append(res.Stages, StageRecord{Stage: string(core.StageAudio), OK: false,
			Errors: []string{perr.Error()}})
		return nil, core.Wrap(core.CodeUnrecoverable, core.StageAudio, false, "audio processing failed", perr)
	}
	res.Stages = append(res.Stages, StageRecord{Stage: string(core.StageAudio), OK: true,
		ValidSamples: processed.Stats.OutputFrames, Gaps: len(tr.Gaps)})

	// Stage: packetize + manifest.
	builder, err := manifest.New(manifest.Config{
		File: opt.File, TargetKbps: opt.TargetKbps, SampleRate: opt.SampleRate,
		Channels: outLayout.NumChannels(), PacketMs: opt.PacketMs,
		PayloadType: 96, SSRC: 0x0BADCAFE,
	}, outLayout)
	if err != nil {
		return nil, err
	}
	outScale := timeline.MustScale(int64(opt.SampleRate))
	for i, seg := range processed.Segments {
		discontinuity := i > 0
		if err := builder.AddSegment(manifest.Segment{
			Audio: seg.Buffer, StartTicks: seg.StartTicks,
			GapsBefore: gapsBefore(tr, seg.StartTicks, outScale), Discontinuity: discontinuity,
		}); err != nil {
			return nil, err
		}
	}
	builder.SetLoudness(processed.Input.IntegratedLUFS, processed.Output.IntegratedLUFS,
		processed.GainDB, processed.Limited)
	builder.SetStats(processed.Stats)
	builder.SetGaps(manifestGaps(tr, outScale))
	for _, w := range tr.Warnings {
		builder.AddStageError(w.Error())
	}
	for _, w := range scan.Warnings {
		builder.AddStageError(w.Error())
	}

	totalMs := int64(0)
	if track != nil && track.Duration > 0 {
		totalMs = timeline.ToMillis(timeline.Ticks(track.Duration), tr.Timescale)
	}
	if totalMs == 0 {
		var end timeline.Ticks
		for _, g := range tr.Gaps {
			if e := g.Start + g.Duration; e > end {
				end = e
			}
		}
		for _, seg := range tr.Valid {
			if e := seg.Start + seg.Len; e > end {
				end = e
			}
		}
		totalMs = timeline.ToMillis(end, tr.Timescale)
	}
	m, err := builder.Finalize(totalMs)
	if err != nil {
		return nil, err
	}
	res.Stages = append(res.Stages, StageRecord{Stage: string(core.StagePacketize), OK: true,
		Packets: m.PacketCount})
	res.Stages = append(res.Stages, StageRecord{Stage: string(core.StageManifest), OK: true})

	if opt.OutPath != "" {
		data, err := manifest.Marshal(m)
		if err != nil {
			return nil, core.Wrap(core.CodeIO, core.StageManifest, false, "encode manifest", err)
		}
		if err := os.WriteFile(opt.OutPath, data, 0o644); err != nil {
			return nil, core.Wrap(core.CodeIO, core.StageManifest, false, "write manifest", err)
		}
	}
	return m, nil
}
