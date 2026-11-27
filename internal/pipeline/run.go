// Package pipeline wires every mediasync subsystem into one deterministic run:
// RTP reconstruction, codec duration parsing, explicit timelines, dependency
// ordering, spec validation, configuration derivation and the audit manifest.
// A failure on one track never aborts independent tracks.
package pipeline

import (
	"mediasync/internal/audio"
	"mediasync/internal/config"
	"mediasync/internal/graph"
	"mediasync/internal/model"
	"mediasync/internal/report"
	"mediasync/internal/rtp"
	"mediasync/internal/spec"
	"mediasync/internal/timeline"
)

// Input is everything needed for one run.
type Input struct {
	Tracks []model.TrackInput
	Target model.TargetSpec
	Seed   int64
	Params report.Params
}

// Run executes the full processing pipeline and returns a stable manifest.
func Run(in Input) report.Manifest {
	// 1. Dependency graph first: it decides which tracks may be configured.
	nodes := make([]graph.Node, 0, len(in.Tracks))
	for _, t := range in.Tracks {
		nodes = append(nodes, graph.Node{TrackID: t.Meta.TrackID, DependsOn: t.DependsOn})
	}
	order := graph.Plan(nodes)
	blocked := map[string]bool{}
	for _, id := range order.Cycle {
		blocked[id] = true
	}
	for id := range order.Unsatisfied {
		blocked[id] = true
	}
	schedulable := map[string]bool{}
	for _, id := range order.Ordered {
		schedulable[id] = true
	}

	byID := map[string]model.TrackInput{}
	for _, t := range in.Tracks {
		byID[t.Meta.TrackID] = t
	}

	// 2. Per-track processing in graph order for stable sequencing.
	reports := make([]report.TrackReport, 0, len(in.Tracks))
	process := func(t model.TrackInput) {
		tl := buildTimeline(t)
		rep := spec.Validate(t.Meta, in.Target)
		decision := config.Derive(t.Meta, tl, rep, in.Target, schedulable[t.Meta.TrackID])
		tr := report.BuildTrackReport(tl, decision, blocked[t.Meta.TrackID])
		reports = append(reports, tr)
	}

	for _, id := range order.Ordered {
		process(byID[id])
	}
	// Any track excluded from the order (cycle/unsatisfied) is still reported.
	handled := map[string]bool{}
	for _, tr := range reports {
		handled[tr.TrackID] = true
	}
	for _, t := range in.Tracks {
		if !handled[t.Meta.TrackID] {
			process(t)
		}
	}

	return report.Build(in.Seed, in.Params, reports)
}

// buildTimeline runs RTP reconstruction and codec duration parsing for a track.
// Any error is converted into a track-level failure so the run always returns
// a usable, fully-populated timeline structure.
func buildTimeline(t model.TrackInput) *model.TrackTimeline {
	tl := &model.TrackTimeline{TrackID: t.Meta.TrackID}

	// Video or packetless tracks keep an empty timeline; their constraints are
	// still validated at the spec stage (e.g. colour-metadata conflicts).
	if len(t.Packets) == 0 {
		return tl
	}

	rec, err := rtp.Reconstruct(t.Meta.TrackID, t.Packets)
	if err != nil {
		if f, ok := err.(model.Failure); ok {
			tl.Failures = append(tl.Failures, f)
		}
		return tl
	}

	var mp4Info *audio.MP4Info
	if len(t.MP4Data) > 0 {
		info, perr := audio.ParseSampleTable(t.Meta.TrackID, t.MP4Data)
		if perr != nil {
			if f, ok := perr.(model.Failure); ok {
				tl.Failures = append(tl.Failures, f)
			}
		} else {
			mp4Info = info
			tl.Failures = append(tl.Failures, info.Failures...)
		}
	}

	return timeline.New().Build(t.Meta, rec, mp4Info)
}
