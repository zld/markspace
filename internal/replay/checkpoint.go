package replay

import (
	"encoding/json"
	"os"
	"path/filepath"

	"replay/internal/fault"
	"replay/internal/manifest"
	"replay/internal/merge"
	"replay/internal/state"
)

// planRef holds the published merge plan.
type planRef struct {
	path   string
	digest string
	plan   *merge.Plan
}

func (e *Engine) markRunning(stage, inputHash string) error {
	cp, _ := e.doc.Stage(stage)
	cp.Name = stage
	cp.Phase = state.PhaseRunning
	cp.InputHash = inputHash
	e.doc.Put(cp)
	return e.store.Save(e.doc)
}

func (e *Engine) markFailed(stage, inputHash string, failure *fault.Error) error {
	cp, _ := e.doc.Stage(stage)
	cp.Name = stage
	cp.Phase = state.PhaseFailed
	cp.InputHash = inputHash
	cp.OutputHash = ""
	cp.RecoveryPosition = 0
	cp.RollbackReason = failure.Reason
	e.doc.Put(cp)
	// Persisting a failure must not mask the original error.
	_ = e.store.Save(e.doc)
	return nil
}

func (e *Engine) markCompleted(stage, inputHash, outputHash string, recovery int, unit string, files []string) error {
	cp, _ := e.doc.Stage(stage)
	cp.Name = stage
	cp.Phase = state.PhaseCompleted
	cp.InputHash = inputHash
	cp.OutputHash = outputHash
	cp.RecoveryPosition = recovery
	cp.RecoveryUnit = unit
	cp.Outputs = append([]string(nil), files...)
	cp.RollbackReason = ""
	e.doc.Put(cp)
	return e.store.Save(e.doc)
}

// recordPriorRollback reports the stages a fresh run invalidated because
// their inputs changed since the previous run. Artefacts were already
// cleared as part of starting fresh, so only the transition is recorded.
func (e *Engine) recordPriorRollback(stage, reason string, res *Result) {
	plan := Plan()
	start := indexOf(plan, stage)
	for _, name := range plan[start:] {
		prev, ok := e.prior.Stage(name)
		if !ok || prev.Phase != state.PhaseCompleted {
			continue
		}
		currentReason := reason
		if name != stage {
			currentReason = "downstream of changed input"
		}
		e.logf("rollback %-8s reason=%s (previous run)\n", name, currentReason)
		res.Transitions = append(res.Transitions,
			Transition{Stage: name, From: state.PhaseCompleted, To: state.PhaseRolledBack})
		e.logf("stage %-8s completed -> rolled-back\n", name)
	}
}

// invalidate rolls a stage and every downstream stage back: outputs are
// removed, phases move to rolled-back and reasons are recorded.
func (e *Engine) invalidate(stage, reason string, res *Result) error {
	plan := Plan()
	index := -1
	for i, name := range plan {
		if name == stage {
			index = i
			break
		}
	}
	if index < 0 {
		return fault.New(fault.KindArgument, "unknown stage "+stage).At("replay")
	}

	for i := len(plan) - 1; i >= index; i-- {
		current := plan[i]
		currentReason := reason
		if i > index {
			currentReason = "downstream of changed input"
		}
		cp, ok := e.doc.Stage(current)
		if !ok || cp.Phase != state.PhaseCompleted {
			continue
		}
		record, err := merge.Rollback(e.WorkDir, current, currentReason, cp.Outputs)
		if err != nil {
			return stageError("rollback", err)
		}
		cp.Phase = state.PhaseRolledBack
		cp.OutputHash = ""
		cp.RecoveryPosition = 0
		cp.RollbackReason = currentReason
		cp.Outputs = nil
		e.doc.Put(cp)
		e.logf("rollback %-8s reason=%s removed=%v\n", current, currentReason, record.Removed)
		if res != nil {
			res.Transitions = append(res.Transitions,
				Transition{Stage: current, From: state.PhaseCompleted, To: state.PhaseRolledBack})
			e.logf("stage %-8s completed -> rolled-back\n", current)
		}
		delete(e.outputs, current)
	}
	// The manifest file itself is an output of the manifest stage only;
	// a rollback of an earlier stage must also drop any stale manifest so
	// a partial result can never be mistaken for a verified one.
	if index <= indexOf(plan, StageManifest) {
		_ = os.Remove(filepath.Join(e.WorkDir, fileManifest))
	}
	return e.store.Save(e.doc)
}

func indexOf(plan []string, stage string) int {
	for i, name := range plan {
		if name == stage {
			return i
		}
	}
	return len(plan)
}

// buildResult verifies the finished manifest, diffs it against the
// previous run and records the new baseline.
func (e *Engine) buildResult(res *Result) error {
	doc, err := manifest.Load(e.path(fileManifest))
	if err != nil {
		return stageError(StageManifest, err)
	}
	res.Manifest = doc
	res.ManifestPath = e.path(fileManifest)
	res.ManifestHash = doc.ArtifactHash
	if bad, verr := manifest.Verify(e.WorkDir, doc); verr != nil {
		return fault.New(fault.KindManifest, "manifest verification failed: "+joinLimited(bad)).
			At(StageManifest).Wrap(verr)
	}
	res.Verified = true

	if e.baseline == nil {
		res.Diff = manifest.Diff{Same: true, RightHash: doc.ArtifactHash}
	} else {
		left := &manifest.Document{ArtifactHash: e.baseline.ManifestHash}
		for path, digest := range e.baseline.Entries {
			left.Entries = append(left.Entries, manifest.Entry{Path: path, SHA256: digest})
		}
		res.Diff = manifest.Compare(left, doc)
	}
	if err := writeBaseline(e.WorkDir, doc); err != nil {
		return stageError(StageManifest, err)
	}
	e.finalManifest = doc
	return nil
}

// readPlan loads the published plan from disk.
func (e *Engine) readPlan() (*planRef, error) {
	path := e.path(filePlan)
	var plan merge.Plan
	if err := readJSON(path, &plan); err != nil {
		return nil, fault.New(fault.KindInput, "cannot read plan artefact").
			At(StageMerge).Wrap(err)
	}
	digest, _, err := fileDigest(path)
	if err != nil {
		return nil, err
	}
	return &planRef{path: filePlan, digest: digest, plan: &plan}, nil
}

// Baseline is the previous completed manifest, kept across runs so the
// CLI can confirm a repeat run changed nothing.
type Baseline struct {
	ManifestHash string            `json:"manifest_hash"`
	Entries      map[string]string `json:"entries"`
}

func readBaseline(dir string) *Baseline {
	var b Baseline
	if err := readJSON(filepath.Join(dir, fileBaseline), &b); err != nil {
		return nil
	}
	if b.Entries == nil {
		b.Entries = map[string]string{}
	}
	return &b
}

func writeBaseline(dir string, doc *manifest.Document) error {
	b := Baseline{ManifestHash: doc.ArtifactHash, Entries: map[string]string{}}
	for _, entry := range doc.Entries {
		b.Entries[entry.Path] = entry.SHA256
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, fileBaseline), append(data, '\n'))
}
