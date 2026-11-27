package replay

import (
	"fmt"
	"io"
	"os"
	"sort"

	"replay/internal/fault"
	"replay/internal/manifest"
	"replay/internal/scenario"
	"replay/internal/state"
)

// Target is the outcome of a run.
type Target int

const (
	// TargetStopped means the run halted after the requested stage.
	TargetStopped Target = iota
	// TargetCompleted means every stage completed and the manifest
	// verified.
	TargetCompleted
)

// Stage names, in execution order.
const (
	StageIngest   = "ingest"
	StageTimeline = "timeline"
	StageGraph    = "graph"
	StageMap      = "map"
	StageMerge    = "merge"
	StageManifest = "manifest"
)

// Plan returns the fixed stage order. Each stage depends on its
// predecessor, so a changed stage invalidates everything after it.
func Plan() []string {
	return []string{StageIngest, StageTimeline, StageGraph, StageMap, StageMerge, StageManifest}
}

// Transition is one logged lifecycle change.
type Transition struct {
	Stage string
	From  state.Phase
	To    state.Phase
}

// Result is the outcome of one run.
type Result struct {
	Outcome      Target
	StoppedAfter string
	// ResumePosition and ResumeUnit say where a stopped run would resume
	// from: the recovery position persisted by the last completed stage.
	ResumePosition int
	ResumeUnit     string
	// Manifest fields are populated when the run completed.
	Manifest     *manifest.Document
	ManifestPath string
	ManifestHash string
	Diff         manifest.Diff
	Verified     bool
	Stages       []state.StageCheckpoint
	Transitions  []Transition
	// ResumedFrom names the first stage actually executed, which for a
	// resumed run is the first incomplete stage.
	ResumedFrom string
	// Reused lists stages served from checkpoints.
	Reused []string
}

// Engine runs the six-stage pipeline for one fixture. It is not safe for
// concurrent use.
type Engine struct {
	// WorkDir is the only directory the engine reads or writes beneath.
	WorkDir string
	// Fixture supplies all inputs and is regenerated deterministically
	// on every invocation, so resume can re-derive every input hash.
	Fixture scenario.Fixture
	// StopAfter names a stage after which the run halts cleanly.
	StopAfter string
	// Resume continues from the first incomplete stage.
	Resume bool
	// Log receives human-readable progress. Nil discards output.
	Log io.Writer

	store    *state.Store
	doc      *state.CheckpointFile
	outputs  map[string]string
	recov    map[string]int
	units    map[string]string
	files    map[string][]string
	baseline *Baseline
	// prior is the checkpoint read before a fresh run cleared artefacts,
	// used to detect and record which stages an input change rolls back.
	prior *state.CheckpointFile
	// packetsProcessed is the ingest watermark. Every strategy stage
	// records it as its recovery position, so a resume point is always
	// expressed in processed packets.
	packetsProcessed int

	ingestArt     *IngestArtifact
	timelineArt   *TimelineArtifact
	graphArt      *GraphArtifact
	mapArt        *MapArtifact
	plan          *planRef
	finalManifest *manifest.Document
}

// Run executes the pipeline. Any error is a structured fault; the caller
// maps it to an exit code with fault.ExitCode.
func (e *Engine) Run() (*Result, error) {
	if e.WorkDir == "" {
		return nil, fault.New(fault.KindArgument, "work directory required").At("replay")
	}
	if err := e.validate(); err != nil {
		return nil, err
	}
	store, err := state.NewStore(e.WorkDir)
	if err != nil {
		return nil, err
	}
	e.store = store
	e.outputs = map[string]string{}
	e.recov = map[string]int{}
	e.units = map[string]string{}
	e.files = map[string][]string{}
	e.baseline = readBaseline(e.WorkDir)

	prior, err := store.Load()
	if err != nil {
		return nil, err
	}
	if e.Resume {
		e.doc = prior
		if e.doc.RunID == "" {
			e.doc.RunID = e.runID()
		}
	} else {
		// Compare against the previous checkpoint before clearing so a
		// changed input can be reported as a rollback of the stages it
		// invalidates, rather than silently rebuilding.
		e.prior = prior
		if err := e.clearArtifacts(); err != nil {
			return nil, err
		}
		e.doc = &state.CheckpointFile{RunID: e.runID(), SchemaVersion: state.CheckpointSchemaVersion}
	}
	return e.execute()
}

func (e *Engine) execute() (*Result, error) {
	res := &Result{Outcome: TargetCompleted}
	ranAny := false

	for _, stage := range Plan() {
		inputHash, err := e.stageInputHash(stage)
		if err != nil {
			return nil, err
		}
		cp, exists := e.doc.Stage(stage)
		if !exists && e.prior != nil {
			if prev, ok := e.prior.Stage(stage); ok && prev.Phase == state.PhaseCompleted && prev.InputHash != inputHash {
				e.recordPriorRollback(stage, "input changed since previous run", res)
			}
		}
		if exists && cp.Phase == state.PhaseCompleted && cp.InputHash != inputHash {
			if err := e.invalidate(stage, "input changed since checkpoint", res); err != nil {
				return nil, err
			}
			exists = false
		}
		if e.Resume && exists && cp.Phase == state.PhaseCompleted && e.artifactPresent(stage) {
			e.loadCached(stage, cp)
			res.Reused = append(res.Reused, stage)
			continue
		}
		if res.ResumedFrom == "" {
			res.ResumedFrom = stage
		}
		ranAny = true
		if err := e.runStage(stage, inputHash, res); err != nil {
			res.Stages = e.sortedStages()
			return nil, err
		}
		if e.StopAfter == stage {
			res.Outcome = TargetStopped
			res.StoppedAfter = stage
			res.ResumePosition = e.recov[stage]
			res.ResumeUnit = e.units[stage]
			res.Stages = e.sortedStages()
			return res, nil
		}
	}

	if !ranAny {
		// Everything was already complete; re-read and re-verify rather
		// than trust the checkpoint.
		e.loadCached(StageManifest, mustStage(e.doc, StageManifest))
	}
	res.Stages = e.sortedStages()
	if err := e.buildResult(res); err != nil {
		return nil, err
	}
	return res, nil
}

func (e *Engine) runStage(stage, inputHash string, res *Result) error {
	if prev, ok := e.doc.Stage(stage); ok && prev.Phase == state.PhaseRolledBack {
		e.logTransition(res, stage, state.PhaseRolledBack, state.PhasePending)
	}
	from := state.PhasePending
	if prev, ok := e.doc.Stage(stage); ok && prev.Phase == state.PhaseFailed {
		from = state.PhaseFailed
	}
	e.logTransition(res, stage, from, state.PhaseRunning)
	if err := e.markRunning(stage, inputHash); err != nil {
		return err
	}

	var (
		outputHash string
		recovery   int
		unit       string
		files      []string
		err        error
	)
	switch stage {
	case StageIngest:
		outputHash, recovery, unit, files, err = e.runIngest()
	case StageTimeline:
		outputHash, recovery, unit, files, err = e.runTimeline()
	case StageGraph:
		outputHash, recovery, unit, files, err = e.runGraph()
	case StageMap:
		outputHash, recovery, unit, files, err = e.runMap()
	case StageMerge:
		outputHash, recovery, unit, files, err = e.runMerge()
	case StageManifest:
		outputHash, recovery, unit, files, err = e.runManifest()
	default:
		err = fault.New(fault.KindArgument, "unknown stage "+stage).At("replay")
	}
	if err != nil {
		fe, ok := fault.As(err)
		if !ok {
			fe = fault.New(fault.KindInput, "stage failed").At(stage).Wrap(err)
		}
		_ = e.markFailed(stage, inputHash, fe)
		return fe
	}

	// Recovery positions are always the processed-packet watermark, so
	// stopping after any stage reports the same resume quantity.
	if stage == StageIngest {
		e.packetsProcessed = recovery
	}
	recovery = e.packetsProcessed
	unit = "packets"
	e.outputs[stage] = outputHash
	e.recov[stage] = recovery
	e.units[stage] = unit
	e.files[stage] = files
	if err := e.markCompleted(stage, inputHash, outputHash, recovery, unit, files); err != nil {
		return err
	}
	e.logTransition(res, stage, state.PhaseRunning, state.PhaseCompleted)
	e.logf("stage %-8s completed output=%s recovered_at=%d %s\n",
		stage, manifest.ShortHash(outputHash), recovery, unit)
	return nil
}

func (e *Engine) loadCached(stage string, cp state.StageCheckpoint) {
	e.outputs[stage] = cp.OutputHash
	e.recov[stage] = cp.RecoveryPosition
	e.units[stage] = cp.RecoveryUnit
	e.files[stage] = append([]string(nil), cp.Outputs...)
	switch stage {
	case StageIngest:
		var art IngestArtifact
		if err := readJSON(e.path(fileIngest), &art); err == nil {
			e.ingestArt = &art
			e.packetsProcessed = art.PacketsProcessed
		}
	case StageTimeline:
		var art TimelineArtifact
		if err := readJSON(e.path(fileTimeline), &art); err == nil {
			e.timelineArt = &art
		}
	case StageGraph:
		var art GraphArtifact
		if err := readJSON(e.path(fileGraph), &art); err == nil {
			e.graphArt = &art
		}
	case StageMap:
		var art MapArtifact
		if err := readJSON(e.path(fileMap), &art); err == nil {
			e.mapArt = &art
		}
	}
}

func (e *Engine) artifactPresent(stage string) bool {
	switch stage {
	case StageIngest:
		return fileExists(e.path(fileIngest))
	case StageTimeline:
		return fileExists(e.path(fileTimeline))
	case StageGraph:
		return fileExists(e.path(fileGraph))
	case StageMap:
		return fileExists(e.path(fileMap))
	case StageMerge:
		return fileExists(e.path("out/plan.json"))
	case StageManifest:
		return fileExists(e.path(fileManifest))
	}
	return false
}

func mustStage(doc *state.CheckpointFile, name string) state.StageCheckpoint {
	cp, _ := doc.Stage(name)
	return cp
}

func (e *Engine) sortedStages() []state.StageCheckpoint {
	out := append([]state.StageCheckpoint(nil), e.doc.Stages...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (e *Engine) validate() error {
	if len(e.Fixture.Tracks) == 0 {
		return fault.New(fault.KindArgument, "fixture has no tracks").At("replay")
	}
	if e.StopAfter != "" {
		for _, stage := range Plan() {
			if stage == e.StopAfter {
				return nil
			}
		}
		return fault.New(fault.KindArgument, "unknown --stop-after stage "+e.StopAfter).At("replay")
	}
	return nil
}

func (e *Engine) runID() string {
	return "run-" + fixtureInputHash(e.Fixture)[:16]
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		fmt.Fprintf(e.Log, format, args...)
	}
}

func (e *Engine) logTransition(res *Result, stage string, from, to state.Phase) {
	if from == to {
		return
	}
	res.Transitions = append(res.Transitions, Transition{Stage: stage, From: from, To: to})
	e.logf("stage %-8s %s -> %s\n", stage, from, to)
}

func (e *Engine) path(name string) string {
	return filepathJoin(e.WorkDir, name)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// loadArtifacts reads every persisted artefact into memory when a stage
// is served from checkpoint but a later stage needs its data.
func (e *Engine) loadArtifacts() error {
	if e.ingestArt == nil {
		var art IngestArtifact
		if err := readJSON(e.path(fileIngest), &art); err != nil {
			return fault.New(fault.KindInput, "cannot read ingest artefact").At("replay").Wrap(err)
		}
		e.ingestArt = &art
	}
	if e.timelineArt == nil {
		var art TimelineArtifact
		if err := readJSON(e.path(fileTimeline), &art); err != nil {
			return fault.New(fault.KindInput, "cannot read timeline artefact").At("replay").Wrap(err)
		}
		e.timelineArt = &art
	}
	if e.graphArt == nil {
		var art GraphArtifact
		if err := readJSON(e.path(fileGraph), &art); err != nil {
			return fault.New(fault.KindInput, "cannot read graph artefact").At("replay").Wrap(err)
		}
		e.graphArt = &art
	}
	if e.mapArt == nil {
		var art MapArtifact
		if err := readJSON(e.path(fileMap), &art); err != nil {
			return fault.New(fault.KindInput, "cannot read map artefact").At("replay").Wrap(err)
		}
		e.mapArt = &art
	}
	if e.plan == nil && fileExists(e.path("out/plan.json")) {
		plan, err := e.readPlan()
		if err != nil {
			return err
		}
		e.plan = plan
	}
	return nil
}

// clearArtifacts removes everything a previous run published except the
// baseline, which records the previous manifest for comparison.
func (e *Engine) clearArtifacts() error {
	entries, err := os.ReadDir(e.WorkDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fault.New(fault.KindInput, "cannot list work directory").At("replay").Wrap(err)
	}
	for _, entry := range entries {
		if entry.Name() == fileBaseline {
			continue
		}
		if err := os.RemoveAll(filepathJoin(e.WorkDir, entry.Name())); err != nil {
			return fault.New(fault.KindInput, "cannot clear work directory").At("replay").Wrap(err)
		}
	}
	return nil
}
