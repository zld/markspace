package main

import (
	"fmt"
	"sort"

	"mediadag/internal/clock"
	"mediadag/internal/gen"
	"mediadag/internal/ingest"
	"mediadag/internal/inspect"
	"mediadag/internal/manifest"
	"mediadag/internal/task"
)

// buildPipeline wires the five-node analysis DAG for one sample. Ingest is
// fed RTP only; RTCP anchors are owned by the timeline node so that adding a
// missing anchor does not invalidate the already-committed media ingest.
func buildPipeline(sample *gen.Sample, paramsJSON []byte, reorderWindow int,
	targetKbps, segmentMs float64, ckpt *task.Checkpoint,
	opts pipelineOptions) (*task.Scheduler, error) {

	ingestNode := &task.Node{
		Name: "ingest", Version: nodeVersion,
		Input: ingestInput(sample, reorderWindow),
		Run: func(map[string]any) (any, error) {
			r := ingest.NewReorderer(reorderWindow)
			for _, ev := range sample.Events {
				if ev.Kind == ingest.KindRTP {
					r.Push(ev)
				}
			}
			events, stats := r.Flush()
			return &IngestResult{Events: events, Stats: stats}, nil
		},
		Decode: decode[IngestResult],
	}

	timelineNode := &task.Node{
		Name: "timeline", Version: nodeVersion,
		Input: timelineInput(sample, opts),
		Run: func(map[string]any) (any, error) {
			tl := clock.NewTimeline()
			for _, ev := range sample.Events {
				if ev.Kind == ingest.KindRTCP && ev.SR != nil {
					tl.AddSenderReport(ev.SR.RTPTime, ntpToTime(ev.SR.NTPTime))
				}
			}
			if !tl.Anchored() {
				return nil, &task.Error{Category: task.CatNoAnchor,
					Msg: "no RTCP sender report: media time uncertain"}
			}
			st := tl.Snapshot()
			return &st, nil
		},
		Decode: decode[clock.State],
	}

	inspectNode := &task.Node{
		Name: "inspect", Deps: []string{"ingest", "timeline"}, Version: nodeVersion,
		Input: inspectInput(sample, opts),
		Run: func(deps map[string]any) (any, error) {
			return runInspect(deps, opts)
		},
		Decode: decode[inspect.Analysis],
	}

	manifestNode := &task.Node{
		Name: "manifest", Deps: []string{"ingest", "inspect", "timeline"}, Version: nodeVersion,
		Input: manifestInput(segmentMs, targetKbps),
		Run: func(deps map[string]any) (any, error) {
			an := deps["inspect"].(*inspect.Analysis)
			in := deps["ingest"].(*IngestResult)
			lossRatio := 0.0
			if total := in.Stats.Received + in.Stats.Lost; total > 0 {
				lossRatio = float64(in.Stats.Lost) / float64(total)
			}
			rep, err := manifest.Build(an, segmentMs, targetKbps, lossRatio)
			if err == manifest.ErrTimeUncertain {
				return nil, &task.Error{Category: task.CatNoAnchor, Msg: err.Error()}
			}
			if err != nil {
				return nil, &task.Error{Category: task.CatInputCorrupt, Msg: err.Error()}
			}
			return rep, nil
		},
		Decode: decode[manifest.Report],
	}

	reportNode := &task.Node{
		Name: "report", Deps: []string{"ingest", "timeline", "inspect", "manifest"},
		Version: nodeVersion, Input: paramsJSON,
		Run: func(deps map[string]any) (any, error) {
			return buildSummary(deps, sample, paramsJSON, targetKbps), nil
		},
		Decode: decode[Summary],
	}

	return task.NewScheduler(
		[]*task.Node{ingestNode, timelineNode, inspectNode, manifestNode, reportNode}, ckpt)
}

// runInspect reassembles NAL units over the committed stream, locates loss
// gaps on the media timeline and, in strict mode, surfaces the first
// truncated or missing fragment as an input-corrupt failure.
func runInspect(deps map[string]any, opts pipelineOptions) (any, error) {
	in := deps["ingest"].(*IngestResult)
	tl := clock.FromSnapshot(*deps["timeline"].(*clock.State))
	an := inspect.NewAnalyzer(tl)

	type pos struct {
		ext int64
		ts  uint32
	}
	positions := make([]pos, 0, len(in.Events))
	for _, ev := range in.Events {
		if ev.Kind != ingest.KindRTP {
			continue
		}
		// committed events carry the reorderer's extended sequence number
		positions = append(positions, pos{ev.Ext, ev.Packet.Timestamp})
		an.Add(ev)
	}
	an.AddLossGaps(in.Stats.Gaps, func(g ingest.Gap) (uint32, bool) {
		i := sort.Search(len(positions), func(i int) bool {
			return positions[i].ext > g.To
		})
		if i < len(positions) {
			return positions[i].ts, true
		}
		if len(positions) > 0 {
			return positions[len(positions)-1].ts, true
		}
		return 0, false
	})
	res := an.Finish()
	if opts.StrictNAL {
		for _, c := range res.Corrupt {
			if c.Reason == "truncated-nal" || c.Reason == "missing-fragment" {
				return nil, &task.Error{Category: task.CatInputCorrupt,
					Msg: fmt.Sprintf("%s at %.1fms", c.Reason, c.StartMs)}
			}
		}
	}
	return res, nil
}

// buildSummary flattens dependency results into the final report.
func buildSummary(deps map[string]any, sample *gen.Sample, paramsJSON []byte,
	targetKbps float64) *Summary {
	in := deps["ingest"].(*IngestResult)
	an := deps["inspect"].(*inspect.Analysis)
	mf := deps["manifest"].(*manifest.Report)
	s := &Summary{
		InputDigest:     sample.Digest,
		Params:          string(paramsJSON),
		Version:         nodeVersion,
		TargetKbps:      targetKbps,
		ActualKbps:      mf.ActualKbps,
		SentKbps:        mf.SentKbps,
		LossRatio:       mf.LossRatio,
		DeviationCause:  mf.DeviationCause(),
		PacketsReceived: in.Stats.Received,
		LossDetected:    in.Stats.Lost,
		Duplicates:      in.Stats.Duplicates,
		Late:            in.Stats.Late,
		IDRCount:        an.IDRCount,
		SPSCount:        an.SPSCount,
		PPSCount:        an.PPSCount,
		CorruptRanges:   len(an.Corrupt),
		ManifestAlignMs: mf.AlignmentErrMs,
		ManifestStartMs: mf.StartMs,
		ManifestEndMs:   mf.EndMs,
		Segments:        len(mf.Segments),
		BitrateDevPct:   mf.BitrateDeviationPct(),
	}
	if n := len(an.KeyframeIntMs); n > 0 {
		for _, iv := range an.KeyframeIntMs {
			s.KeyframeIntAvgMs += iv
		}
		s.KeyframeIntAvgMs /= float64(n)
	}
	s.QualityScore = qualityScore(in.Stats, an, mf)
	return s
}

// qualityScore combines loss, corruption, alignment and bitrate deviation
// into a deterministic 0-100 score.
func qualityScore(st ingest.Stats, an *inspect.Analysis, mf *manifest.Report) float64 {
	score := 100.0
	if st.Received > 0 {
		lossPct := float64(st.Lost) / float64(st.Received+st.Lost) * 100
		score -= min(40, lossPct*4)
	}
	score -= min(25, float64(len(an.Corrupt))*1.0)
	score -= min(20, mf.AlignmentErrMs*2)
	score -= min(15, mf.BitrateDeviationPct())
	if score < 0 {
		score = 0
	}
	return score
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// runPipeline executes the DAG for one sample against the given checkpoint.
func runPipeline(ckpt *task.Checkpoint, sample *gen.Sample, paramsJSON []byte,
	reorderWindow int, targetKbps, segmentMs float64, opts pipelineOptions) (*task.Report, error) {
	sched, err := buildPipeline(sample, paramsJSON, reorderWindow, targetKbps, segmentMs, ckpt, opts)
	if err != nil {
		return nil, err
	}
	return sched.Run(), nil
}
