package pipeline

import (
	"encoding/json"
	"path/filepath"

	"replaycheck/internal/derive"
	"replaycheck/internal/ingest"
	"replaycheck/internal/job"
	"replaycheck/internal/manifest"
	"replaycheck/internal/model"
	"replaycheck/internal/policy"
	"replaycheck/internal/report"
	"replaycheck/internal/split"
	"replaycheck/internal/store"
	"replaycheck/internal/timeline"
)

// step is one registered phase. Registering from the stage's own file keeps
// each phase self-contained and lets the pipeline grow one stage at a time.
type step struct {
	stage model.Stage
	fn    func() error
}

var registry []step

func register(stage model.Stage, fn func() error) {
	registry = append(registry, step{stage, fn})
}

// sortedSteps returns registered phases in canonical order.
func sortedSteps() []step {
	out := append([]step(nil), registry...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].stage.Index() < out[j-1].stage.Index(); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

type trackSummary struct {
	ID        uint32 `json:"id"`
	Kind      string `json:"kind"`
	Codec     string `json:"codec"`
	Timescale uint32 `json:"timescale"`
	Duration  uint64 `json:"duration"`
	Samples   int    `json:"samples"`
	Bytes     uint64 `json:"bytes"`
}

type ingestSummary struct {
	MediaDigest string         `json:"mediaDigest"`
	EventDigest string         `json:"eventDigest"`
	BoxCount    int            `json:"boxCount"`
	MediaSize   uint64         `json:"mediaSize"`
	Tracks      []trackSummary `json:"tracks"`
	Events      int            `json:"events"`
	Wraps       uint64         `json:"wraps"`
}

type run struct {
	cfg       Config
	out       *report.Writer
	runner    *job.Runner
	resumeAt  model.Stage
	clockRate uint32

	mediaPath, eventPath   string
	mediaAsset, eventAsset store.Source
	siteDir                string
	cache                  *store.Derived
	blobs                  *store.BlobStore

	cont   *ingest.Container
	events *ingest.EventFile
	tl     *timeline.Timeline
	plan   *split.Plan
	pres   *policy.Result
	clips  []derive.Clip
	man    *manifest.Manifest

	ingestOut, normOut, splitOut, deriveOut, manifestOut string
	result                                               *Result
}

// execute runs every registered phase in order. An injected failure happens
// before its phase runs, leaving the previous phase as the recovery point.
func (r *run) execute() error {
	r.cache = store.NewDerived(filepath.Join(r.siteDir, "cache"))
	r.blobs = store.NewBlobStore(filepath.Join(r.siteDir, "blobs"))
	r.result = &Result{Fixture: string(r.cfg.Kind), SourceDigest: r.mediaAsset.Digest}
	for _, s := range sortedSteps() {
		if r.cfg.FailAt == s.stage {
			return r.injectedFailure(s.stage)
		}
		if err := s.fn(); err != nil {
			return err
		}
	}
	r.result.Stages = len(r.runner.State.Records)
	return nil
}

func (r *run) injectedFailure(stage model.Stage) error {
	off := uint64(0)
	if r.plan != nil && len(r.plan.Segments) > 0 && len(r.plan.Segments[0].Ranges) > 0 {
		off = r.plan.Segments[0].Ranges[0].Offset
	}
	return model.NewError(stage, "inject", model.TrackRef{Kind: "fixture"}, off, "injected interruption for recovery test")
}

// stageOutput recovers the digest a successful phase recorded, which a resumed
// run uses instead of recomputing that phase.
func (r *run) stageOutput(stage model.Stage) (string, bool) {
	rec, ok := r.runner.Record(stage)
	if !ok || rec.OutputSum == "" {
		return "", false
	}
	return rec.OutputSum, true
}

func (r *run) skipped(stage model.Stage) bool {
	return r.runner.Skipped(stage, r.resumeAt)
}

func (r *run) fetch(kind, input string, dst any) bool {
	raw, ok, err := r.cache.Get(store.Key(kind, input))
	if err != nil || !ok {
		return false
	}
	return json.Unmarshal(raw, dst) == nil
}

func (r *run) fetchIfSkipped(stage model.Stage, kind, input string, dst any) (string, bool) {
	if !r.skipped(stage) {
		return "", false
	}
	out, ok := r.stageOutput(stage)
	if !ok {
		return "", false
	}
	if !r.fetch(kind, input, dst) {
		return "", false
	}
	return out, true
}

func (r *run) save(kind, input string, value any) error {
	return r.cache.Put(store.Key(kind, input), value)
}

func (r *run) record(stage model.Stage, input string, cached bool) {
	r.runner.Begin(stage, input, cached, r.resumeAt)
	r.out.Emit(report.KindStage, map[string]any{
		"stage": stage, "index": stage.Index(), "cached": cached,
		"resumed": r.skipped(stage), "inputDigest": input, "resumeAt": r.resumeAt,
	})
}

func (r *run) finish(stage model.Stage, output string) {
	r.runner.Complete(stage, output)
}

func (r *run) container() (*ingest.Container, error) {
	if r.cont != nil {
		return r.cont, nil
	}
	c, err := ingest.ParseContainer(r.mediaAsset.Path, r.cfg.Limits)
	if err != nil {
		return nil, err
	}
	r.cont = c
	return c, nil
}

func (r *run) eventFile() (*ingest.EventFile, error) {
	if r.events != nil {
		return r.events, nil
	}
	ev, err := ingest.ParseEvents(r.eventAsset.Path, r.clockRate)
	if err != nil {
		return nil, err
	}
	r.events = ev
	return ev, nil
}
