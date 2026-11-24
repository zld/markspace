// Package pipeline wires every subsystem together in dependency order. It
// scans the container, builds unified timelines, plans track recovery
// topologically, analyzes keyframes, localizes damage, recovers RTP, aligns
// A/V and emits the quantitative report. Stage latency is modeled
// deterministically from work units (samples/packets), not wall clock, so a
// fixed fixture always yields the same summary; these numbers characterize
// the fixed assets and are not real-time capacity claims.
package pipeline

import (
	"os"
	"path/filepath"

	"mediacheck/internal/bitstream"
	"mediacheck/internal/container"
	"mediacheck/internal/damage"
	"mediacheck/internal/fixture"
	"mediacheck/internal/media"
	"mediacheck/internal/report"
	rtppkg "mediacheck/internal/rtp"
	"mediacheck/internal/sync"
	"mediacheck/internal/track"
)

// Outcome is everything a run produced, including the final metrics.
type Outcome struct {
	Case     fixture.Case
	Metrics  report.Metrics
	Plan     track.Plan
	Lines    []*media.TrackLine
	Keyframes []bitstream.Report
	Damages  []damage.Error
	RTP      *rtppkg.Result
	Sync     *sync.Report
}

// Run executes the full kernel over one generated case. workDir holds the
// transient container file; the source spec is never rewritten.
func Run(c fixture.Case, workDir string) (*Outcome, error) {
	lat := newLatencyModel()
	out := &Outcome{Case: c}

	// Stage 1: container scan.
	path := filepath.Join(workDir, string(c.Name)+".mp4")
	if err := os.WriteFile(path, c.MP4, 0o600); err != nil {
		return nil, err
	}
	scan, err := container.Scan(path)
	if err != nil {
		return nil, err
	}
	lat.stage("scan", len(scan.Tracks), costScan)

	// Stage 2: dependency graph + topological plan.
	graph := track.NewGraph()
	for _, tref := range scan.Pre.Trefs {
		graph.AddEdge(track.Edge{
			TrackID: tref.FromTrackID, DependsOn: tref.ToTrackID,
			Kind: track.KindFromString(tref.RefType),
		})
	}
	for _, tm := range scan.Tracks {
		graph.AddTrack(tm.TrackID)
	}
	out.Plan = graph.Plan()
	stopped := map[uint32]bool{}
	for _, s := range out.Plan.Stopped {
		stopped[s.TrackID] = true
		out.Damages = append(out.Damages, damage.Error{
			Code: s.Code, TrackID: s.TrackID, Detail: s.Detail,
			Range:       damage.Range{Deterministic: true, Recoverable: damage.RecoverableNo},
			Recoverable: damage.RecoverableNo,
		})
	}
	lat.stage("graph", len(out.Plan.Order), costGraph)

	// Stage 3: unified timelines for every planned track.
	var lines []*media.TrackLine
	lineByID := map[uint32]*media.TrackLine{}
	for _, tm := range scan.Tracks {
		if stopped[tm.TrackID] {
			continue // never silently cross a stopped dependency
		}
		tb, err := media.NewTimeBase(tm.Timescale)
		if err != nil {
			return nil, err
		}
		l, err := media.BuildTrackLine(tm.TrackID, tm.Kind(), tb, tm.RawSamples, tm.Edits, tm.Index.Undecidable)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l)
		lineByID[tm.TrackID] = l
		lat.stage("timeline", len(l.Points), costTimeline)
	}
	out.Lines = lines

	// Stage 4: keyframe analysis per track.
	var kfs []bitstream.Report
	for _, l := range lines {
		if l.Kind != media.KindVideo {
			continue
		}
		rep := bitstream.Analyze(l)
		kfs = append(kfs, rep)
		lat.stage("keyframe", len(rep.Boundaries), costKeyframe)
	}
	out.Keyframes = kfs

	// Stage 5: damage localization on the unified timeline.
	locator := damage.NewLocator(lines)
	out.Damages = append(out.Damages, locator.LocateFromScan(scan)...)
	lat.stage("damage", len(out.Damages), costDamage)

	// Stage 6: RTP recovery when the case carries packets.
	if len(c.RTP) > 0 && c.RTPSpec != nil {
		res, err := rtppkg.Recover(c.RTP, c.RTPSpec.ClockRate, 8)
		if err != nil {
			return nil, err
		}
		out.RTP = res
		out.Damages = append(out.Damages, res.Errors...)
		lat.stage("rtp", len(res.Ordered), costRTP)
	}

	// Stage 7: A/V alignment across the surviving tracks.
	var vline, aline *media.TrackLine
	for _, l := range lines {
		switch l.Kind {
		case media.KindVideo:
			vline = l
		case media.KindAudio:
			aline = l
		}
	}
	if vline != nil && aline != nil {
		var badRanges []damage.Range
		for _, d := range out.Damages {
			if d.Recoverable == damage.RecoverableNo {
				badRanges = append(badRanges, d.Range)
			}
		}
		out.Sync = sync.Align(vline, aline, badRanges)
		lat.stage("sync", len(out.Sync.Measurements), costSync)
	}

	out.Metrics = report.Build(report.Input{
		Lines: lines, Keyframes: kfs, Damages: out.Damages,
		RTP: out.RTP, Sync: out.Sync, Latencies: lat.samples(),
	})
	return out, nil
}
