package stage

import (
	"rtptrace/internal/fault"
)

// Stage names used by the pipeline. Order here defines execution order.
const (
	NameScan   = "scan"   // container parse -> parsed sample summary
	NameRTP    = "rtp"    // build + simulate + reassemble RTP trace
	NameMerge  = "merge"  // merge into manifest + artifact
)

// OrderedNames is the fixed pipeline order.
var OrderedNames = []string{NameScan, NameRTP, NameMerge}

// Executor produces one stage's artifact bytes from the current state and any
// already-verified upstream artifacts. It must be deterministic: identical
// inputs always yield identical bytes.
type Executor func(ctx *RunContext) (relPath string, data []byte, err error)

// RunContext gives an executor access to persisted upstream artifacts.
type RunContext struct {
	State *State
	Store *Store
}

// ReadUpstream returns the verified artifact bytes of a prior stage.
func (c *RunContext) ReadUpstream(name string) ([]byte, error) {
	rec, ok := c.State.Stages[name]
	if !ok {
		return nil, fault.Newf(fault.KindInternal, name, fault.NoOffset, "unknown stage")
	}
	return c.Store.ReadArtifact(rec)
}

// Runner drives ordered, resumable execution.
type Runner struct {
	store     *Store
	executors map[string]Executor
	// InterruptAfter, when set to a stage name, aborts once that stage has
	// been marked running but before it commits, simulating a crash.
	InterruptAfter string
	interrupted    bool
}

// NewRunner wires executors to their stage names.
func NewRunner(store *Store, executors map[string]Executor) *Runner {
	return &Runner{store: store, executors: executors}
}

// Result summarizes one Run call for the CLI adapter.
type Result struct {
	Recovered       bool
	ResumedAt       string
	ResumedFromMS   int64
	RanStages       []string
	SkippedStages   []string
	Interrupted     bool
	InterruptedName string
}

// Run executes every stage in order, reusing verified checkpoints. A corrupt
// or missing artifact causes only that stage (and its dependents, by virtue of
// ordering) to be redone.
func (r *Runner) Run(st *State) (*Result, error) {
	res := &Result{}
	for _, name := range OrderedNames {
		rec := st.Stages[name]
		if err := r.store.VerifyDone(rec); err == nil {
			res.SkippedStages = append(res.SkippedStages, name)
			continue
		}

		// First stage actually executed means we are recovering prior work.
		if len(res.RanStages) == 0 && len(res.SkippedStages) > 0 {
			res.Recovered = true
			res.ResumedAt = name
			res.ResumedFromMS = rec.StartedMS
		}

		if err := r.store.MarkRunning(st, name); err != nil {
			return res, err
		}
		if r.InterruptAfter == name && !r.interrupted {
			r.interrupted = true
			res.Interrupted = true
			res.InterruptedName = name
			return res, nil
		}

		exec, ok := r.executors[name]
		if !ok {
			err := fault.Newf(fault.KindInternal, name, fault.NoOffset, "no executor registered")
			_ = r.store.MarkFailed(st, name, err)
			return res, err
		}
		relPath, data, err := exec(&RunContext{State: st, Store: r.store})
		if err != nil {
			_ = r.store.MarkFailed(st, name, err)
			return res, err
		}
		if _, err := r.store.CommitArtifact(st, name, relPath, data); err != nil {
			_ = r.store.MarkFailed(st, name, err)
			return res, err
		}
		res.RanStages = append(res.RanStages, name)
	}
	return res, nil
}
