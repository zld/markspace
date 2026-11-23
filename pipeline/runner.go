package pipeline

import (
	"fmt"
	"path/filepath"

	"mediadist/demux"
	"mediadist/normalize"
	"mediadist/segment"
	"mediadist/timeline"
)

// Input is one representation's self-generated container.
type Input struct {
	ID               string
	TargetBitrateBps uint64
	Fixture          []byte
	// GraphFault is applied after parsing (timestamp-back, missing-keyframe).
	GraphFault demux.FaultCase
}

// Result is the verified output of a complete pipeline run.
type Result struct {
	Manifest     *segment.Manifest
	Reports      map[string]*segment.BitrateReport
	AudioPlans   map[string]*normalize.AudioPlan
	ColorSources map[string]timeline.ColorSource
	Stages       []StageState
}

// Runner executes the ordered stages against a checkpoint store.
type Runner struct {
	opts  Options
	store *Store
}

// NewRunner builds a runner whose checkpoints live under opts.OutputDir.
func NewRunner(opts Options) (*Runner, error) {
	if opts.OutputDir == "" {
		return nil, asRunError(StageDemux, fmt.Errorf("output directory required"))
	}
	if opts.Segment.TargetSegmentTicks == 0 {
		opts.Segment = segment.DefaultConfig()
	}
	store, err := NewStore(filepath.Join(opts.OutputDir, ".checkpoint"))
	if err != nil {
		return nil, asRunError(StageDemux, err)
	}
	return &Runner{opts: opts, store: store}, nil
}

// Store exposes the checkpoint store for recovery tooling.
func (r *Runner) Store() *Store { return r.store }

// processedGraph pairs a parsed graph with its representation metadata.
type processedGraph struct {
	input Input
	graph *timeline.Graph
	rep   segment.Representation
}

// Run executes every stage. On a stage failure it persists a failed
// checkpoint (never a partial artifact) and returns a RunError.
func (r *Runner) Run(inputs []Input) (*Result, error) {
	if len(inputs) == 0 {
		return nil, asRunError(StageDemux, fmt.Errorf("at least one input is required"))
	}
	res := &Result{
		Reports:      map[string]*segment.BitrateReport{},
		AudioPlans:   map[string]*normalize.AudioPlan{},
		ColorSources: map[string]timeline.ColorSource{},
	}

	// Stage 1: demux + timeline validation.
	processed, err := r.stageDemux(inputs)
	if err != nil {
		r.recordFailure(StageDemux, err)
		return nil, err
	}
	if err := r.store.MarkCompleted(StageDemux); err != nil {
		return nil, asRunError(StageDemux, err)
	}

	// Stage 2: normalize audio and color.
	if err := r.stageNormalize(processed, res); err != nil {
		r.recordFailure(StageNormalize, err)
		return nil, err
	}
	if err := r.store.MarkCompleted(StageNormalize); err != nil {
		return nil, asRunError(StageNormalize, err)
	}

	// Stage 3: align boundaries and measure bitrate.
	plan, err := r.stageSegment(processed, res)
	if err != nil {
		r.recordFailure(StageSegment, err)
		return nil, err
	}
	if err := r.store.MarkCompleted(StageSegment); err != nil {
		return nil, asRunError(StageSegment, err)
	}

	// Stage 4: build and validate the manifest, then commit it atomically.
	if err := r.stageManifest(processed, plan, res); err != nil {
		r.recordFailure(StageManifest, err)
		return nil, err
	}
	if err := r.store.MarkCompleted(StageManifest); err != nil {
		return nil, asRunError(StageManifest, err)
	}
	res.Stages = r.store.Snapshot()
	return res, nil
}

func (r *Runner) stageDemux(inputs []Input) ([]processedGraph, error) {
	out := make([]processedGraph, 0, len(inputs))
	for _, in := range inputs {
		g, err := demux.ParseBytes(in.Fixture)
		if err != nil {
			return nil, asRunError(StageDemux, err)
		}
		if in.GraphFault != demux.FaultNone {
			demux.InjectGraphFault(g, in.GraphFault)
		}
		if err := timeline.ValidateGraph(g); err != nil {
			return nil, asRunError(StageTimeline, err)
		}
		rep := segment.Representation{
			ID:               in.ID,
			TargetBitrateBps: in.TargetBitrateBps,
			Video:            g.ByKind(timeline.KindVideo),
			Audio:            g.ByKind(timeline.KindAudio),
		}
		out = append(out, processedGraph{input: in, graph: g, rep: rep})
	}
	return out, nil
}

func (r *Runner) stageNormalize(processed []processedGraph, res *Result) error {
	for _, pg := range processed {
		// Audio normalization plan.
		if a := pg.graph.ByKind(timeline.KindAudio); a != nil {
			plan, err := normalize.PlanAudio(a, r.opts.TargetAudioRate)
			if err != nil {
				return asRunError(StageNormalize, err)
			}
			res.AudioPlans[pg.input.ID] = plan
		}
		// Color arbitration; a pending decision blocks publication.
		if v := pg.graph.ByKind(timeline.KindVideo); v != nil {
			dec, err := normalize.ArbitrateColor(v, r.opts.CLIColor, r.opts.ColorPriority)
			if err != nil {
				return asRunError(StageNormalize, err)
			}
			if err := normalize.RequireResolved(dec); err != nil {
				return asRunError(StageNormalize, err)
			}
			res.ColorSources[pg.input.ID] = dec.Source
		}
	}
	return nil
}

func (r *Runner) stageSegment(processed []processedGraph, res *Result) (*segment.Plan, error) {
	reps := make([]segment.Representation, 0, len(processed))
	for _, pg := range processed {
		reps = append(reps, pg.rep)
	}
	plan, err := segment.Align(reps, r.opts.Segment)
	if err != nil {
		return nil, asRunError(StageSegment, err)
	}
	for _, rep := range reps {
		report, err := segment.MeasureBitrate(rep, plan, r.opts.Segment)
		if err != nil {
			return nil, asRunError(StageSegment, err)
		}
		res.Reports[rep.ID] = report
	}
	return plan, nil
}

func (r *Runner) stageManifest(processed []processedGraph, plan *segment.Plan, res *Result) error {
	reps := make([]segment.Representation, 0, len(processed))
	audio := map[string]segment.AudioSummary{}
	for _, pg := range processed {
		reps = append(reps, pg.rep)
		if ap, ok := res.AudioPlans[pg.input.ID]; ok {
			audio[pg.input.ID] = segment.AudioSummary{
				ChannelsIn: ap.ChannelsIn, ChannelsOut: ap.ChannelsOut,
				RateIn: ap.RateIn, RateOut: ap.RateOut,
			}
		}
	}
	manifest, err := segment.Build(segment.BuildInput{
		Reps: reps, Plan: plan, Reports: res.Reports,
		ColorSources: res.ColorSources, Audio: audio,
		SSRC: 0x5A17, PayloadType: 96,
	}, r.opts.Segment)
	if err != nil {
		return asRunError(StageManifest, err)
	}
	// Commit only after the manifest passed every internal validation.
	if err := r.store.CommitManifest(r.opts.OutputDir, manifest); err != nil {
		return asRunError(StageManifest, err)
	}
	res.Manifest = manifest
	return nil
}

func (r *Runner) recordFailure(stage string, err error) {
	re := asRunError(stage, err)
	_ = r.store.MarkFailed(re.Stage, re.Code, re.Error())
}
