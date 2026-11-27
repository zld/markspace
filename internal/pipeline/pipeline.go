// Package pipeline wires the three collaboration subsystems together:
// ingest/parse, timeline/dependency, and stage/manifest delivery. Each step is
// a resumable stage, so an interrupted run resumes without redoing work or
// rewriting deliverables. All inter-stage state is persisted as artifacts and
// rebuilt on restart rather than held only in memory.
package pipeline

import (
	"os"
	"path/filepath"

	"mediad/internal/fixture"
	"mediad/internal/ingest"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/plan"
	"mediad/internal/quality"
	"mediad/internal/stage"
	"mediad/internal/timeline"
)

// Stage names used for interruption and state records.
const (
	StageIngest    = "ingest"
	StageNormalize = "normalize"
	StagePlan      = "plan"
	StageExport    = "export"
)

// Artifact paths relative to the run directory.
const (
	capRel      = "artifacts/ingest.packets"
	timelineRel = "artifacts/normalize.timeline"
	decisionRel = "artifacts/plan.decision"
)

// Config controls one demo run.
type Config struct {
	OutDir         string
	Scenario       string
	TargetBitrate  uint64
	MinQuality     float64
	CPUBudgetNS    int64
	ByteBudget     int64
	MaxDeliveryB   int64
	InterruptAfter string
}

// Result carries the final manifest and quality report.
type Result struct {
	Manifest *manifest.Manifest
	Report   *quality.Report
}

// trackRun is the working state rebuilt from persisted artifacts.
type trackRun struct {
	spec     ingest.TrackSpec
	packets  []*ingest.AudioPacket
	timeline *timeline.Timeline
	gaps     []timeline.Gap
	dups     uint64
	accepted uint64
	action   plan.Action
}

// Pipeline executes the four-stage resumable job.
type Pipeline struct {
	cfg    Config
	runner *stage.Runner
}

// New builds a pipeline rooted at cfg.OutDir.
func New(cfg Config, runID string) (*Pipeline, error) {
	r, err := stage.NewRunner(cfg.OutDir, runID, plan.AlgorithmVersion)
	if err != nil {
		return nil, err
	}
	if cfg.InterruptAfter != "" {
		r.SetInterruptAfter(cfg.InterruptAfter)
	}
	return &Pipeline{cfg: cfg, runner: r}, nil
}

// IsInterrupted exposes the controlled interruption sentinel.
func IsInterrupted(err error) bool { return stage.IsInterrupted(err) }

// Run executes every stage in order with summary-based reuse.
func (p *Pipeline) Run() (*Result, error) {
	if err := p.stageIngest(); err != nil {
		return nil, err
	}
	if err := p.stageNormalize(); err != nil {
		return nil, err
	}
	if err := p.stagePlan(); err != nil {
		return nil, err
	}
	if err := p.stageExport(); err != nil {
		return nil, err
	}
	m, err := manifest.Verify(p.cfg.OutDir)
	if err != nil {
		return nil, err
	}
	rep := m.Quality
	return &Result{Manifest: m, Report: &rep}, nil
}

// specForScenario resolves the track spec matching a scenario.
func specForScenario(scenario string) ingest.TrackSpec {
	specs := fixture.Specs()
	if scenario == fixture.ScenTruncAAC {
		return specs[1]
	}
	return specs[0]
}

// parseCapture runs the full ingest path and returns parsed packets.
func parseCapture(spec ingest.TrackSpec, dgrams [][]byte) ([]*ingest.AudioPacket, *timeline.Reorderer, error) {
	r := timeline.NewReorderer(spec.ID, 64)
	var packets []*ingest.AudioPacket
	for _, dg := range dgrams {
		pkt, err := ingest.ParseRTP(spec, dg, 0)
		if err != nil {
			return nil, nil, err
		}
		if err = ingest.Parse(spec, pkt); err != nil {
			return nil, nil, err
		}
		res := r.Push(pkt)
		if res.Late != nil {
			return nil, nil, res.Late
		}
		packets = append(packets, res.Flushed...)
	}
	tail, _ := r.Flush()
	packets = append(packets, tail...)
	return packets, r, nil
}

func (p *Pipeline) stageIngest() error {
	if _, err := fixture.WriteAll(p.cfg.OutDir); err != nil {
		return err
	}
	path := filepath.Join(p.cfg.OutDir, "fixtures", p.cfg.Scenario+".rtp")
	raw, err := os.ReadFile(path)
	if err != nil {
		return model.ErrUsage.At("", 0, StageIngest, 0, "unknown scenario "+p.cfg.Scenario)
	}
	dgrams, err := fixture.SplitDatagrams(raw)
	if err != nil {
		return err
	}
	spec := specForScenario(p.cfg.Scenario)
	inputSum := stage.Summarize("capture", map[string]string{
		"scenario":  p.cfg.Scenario,
		"datagrams": itoa(len(dgrams)),
		"track":     spec.ID,
	})
	task := stage.Task{
		Name: StageIngest, InputSummary: inputSum, ParamSummary: spec.ID,
		Budget: plan.Budget{MaxCPUNanos: p.cfg.CPUBudgetNS, MaxBytes: p.cfg.ByteBudget},
		Run: func(ctx *stage.Context) ([]string, error) {
			packets, reorder, err := parseCapture(spec, dgrams)
			if err != nil {
				return nil, err
			}
			if err := savePackets(ctx.Dir, capRel, spec, packets, reorder); err != nil {
				return nil, err
			}
			ctx.Guard.Add(0, int64(len(raw)))
			return []string{capRel}, nil
		},
	}
	return p.runner.Run(task)
}

func (p *Pipeline) stageNormalize() error {
	spec, packets, gaps, dups, accepted, _, err := loadPackets(p.cfg.OutDir, capRel)
	if err != nil {
		return err
	}
	inputSum := stage.Summarize("normalize", map[string]string{
		"track":    spec.ID,
		"accepted": itoa(int(accepted)),
		"gaps":     itoa(len(gaps)),
	})
	task := stage.Task{
		Name: StageNormalize, InputSummary: inputSum, ParamSummary: "timeline-v1",
		Budget: plan.Budget{MaxCPUNanos: p.cfg.CPUBudgetNS, MaxBytes: p.cfg.ByteBudget},
		Run: func(ctx *stage.Context) ([]string, error) {
			tl, err := timeline.BuildTimeline(spec, packets, gaps, dups)
			if err != nil {
				return nil, err
			}
			if err := saveTimeline(ctx.Dir, timelineRel, tl); err != nil {
				return nil, err
			}
			ctx.Guard.Add(0, int64(len(tl.Frames)*8))
			return []string{timelineRel}, nil
		},
	}
	return p.runner.Run(task)
}

func (p *Pipeline) stagePlan() error {
	tl, err := loadTimeline(p.cfg.OutDir, timelineRel)
	if err != nil {
		return err
	}
	_, _, _, _, _, objectiveQuality, err := loadPackets(p.cfg.OutDir, capRel)
	if err != nil {
		//nolint:govet // shadowing is intentional for stage-local errors
		return err
	}
	// Candidate quality is an objective property of the material, never the
	// requested threshold; the gate in plan.Choose decides feasibility.
	cands := []plan.Candidate{{
		Name: "keep", BitrateBPS: p.cfg.TargetBitrate, Quality: objectiveQuality,
		EstCPUNanos: 1000, EstBytes: estimateBytes(tl, p.cfg.TargetBitrate),
	}}
	inputSum := stage.Summarize("plan", map[string]string{
		"target":  itoa(int(p.cfg.TargetBitrate)),
		"quality": ftoa(p.cfg.MinQuality),
	})
	task := stage.Task{
		Name: StagePlan, InputSummary: inputSum, ParamSummary: plan.AlgorithmVersion,
		Budget: plan.Budget{MaxCPUNanos: p.cfg.CPUBudgetNS, MaxBytes: p.cfg.ByteBudget},
		Run: func(ctx *stage.Context) ([]string, error) {
			decision, err := plan.Choose(cands, plan.Constraints{
				MinQuality:    p.cfg.MinQuality,
				CPUBudgetNS:   p.cfg.CPUBudgetNS,
				ByteBudget:    p.cfg.ByteBudget,
				MaxDeliveryB:  p.cfg.MaxDeliveryB,
				TargetBitrate: p.cfg.TargetBitrate,
			})
			if err != nil {
				return nil, err
			}
			if err := saveDecision(ctx.Dir, decisionRel, decision); err != nil {
				return nil, err
			}
			return []string{decisionRel}, nil
		},
	}
	return p.runner.Run(task)
}

func (p *Pipeline) stageExport() error {
	spec, packets, gaps, dups, accepted, _, err := loadPackets(p.cfg.OutDir, capRel)
	if err != nil {
		return err
	}
	tl, err := loadTimeline(p.cfg.OutDir, timelineRel)
	if err != nil {
		return err
	}
	decision, err := loadDecision(p.cfg.OutDir, decisionRel)
	if err != nil {
		return err
	}
	inputSum := stage.Summarize("export", map[string]string{
		"frames": itoa(len(tl.Frames)),
		"action": string(decision.Action),
	})
	tr := &trackRun{
		spec: spec, packets: packets, timeline: tl, gaps: gaps,
		dups: dups, accepted: accepted, action: decision.Action,
	}
	task := stage.Task{
		Name: StageExport, InputSummary: inputSum, ParamSummary: manifest.ManifestVersion,
		Budget: plan.Budget{MaxCPUNanos: p.cfg.CPUBudgetNS, MaxBytes: p.cfg.ByteBudget},
		Run: func(ctx *stage.Context) ([]string, error) {
			return p.writeDeliverables(ctx, tr)
		},
	}
	return p.runner.Run(task)
}

var _ = quality.MetricAlgorithm
