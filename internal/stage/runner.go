package stage

import (
	"time"

	"mediad/internal/model"
	"mediad/internal/plan"
)

// Task is the unit executed by a Runner. The function must be idempotent:
// repeated delivery with the same inputs must produce byte-identical outputs
// and must not append duplicate manifest entries.
type Task struct {
	Name         string
	InputSummary string
	ParamSummary string
	Budget       plan.Budget
	// Run performs the work and returns the relative output paths created.
	Run func(ctx *Context) ([]string, error)
}

// Context gives a Task its run directory and a live budget guard.
type Context struct {
	Dir   string
	Guard *plan.Guard
	Stage string
}

// Runner executes tasks in registration order, skipping stages already
// completed with identical summaries and persisting state after every stage so
// an interrupted run can resume without redoing work.
type Runner struct {
	store     *Store
	state     *State
	interrupt string // stage name after which to simulate an interruption
}

// NewRunner loads or initializes run state in dir.
func NewRunner(dir, runID, algVersion string) (*Runner, error) {
	st := &Store{Dir: dir}
	state, err := st.Load()
	if err != nil {
		return nil, err
	}
	if state.RunID == "" {
		state.RunID = runID
		state.AlgVersion = algVersion
	}
	return &Runner{store: st, state: state}, nil
}

// SetInterruptAfter makes Run stop (without failing) once the named stage
// completes, leaving resumable persisted state behind it.
func (r *Runner) SetInterruptAfter(stageName string) { r.interrupt = stageName }

// State exposes the persisted state for reporting.
func (r *Runner) State() *State { return r.state }

// Reusable reports whether a completed stage can be reused: it must exist, be
// completed, and carry identical input and parameter summaries.
func (r *Runner) Reusable(t Task) bool {
	existing, ok := r.state.Stages[t.Name]
	if !ok || existing.Status != StatusCompleted {
		return false
	}
	return existing.InputSummary == t.InputSummary && existing.ParamSummary == t.ParamSummary
}

// Run executes one task, honoring reuse, budget and interruption semantics.
func (r *Runner) Run(t Task) error {
	if _, known := r.state.Stages[t.Name]; !known {
		r.state.Stages[t.Name] = &Stage{Name: t.Name, Status: StatusPending}
		r.state.Order = append(r.state.Order, t.Name)
	}
	st := r.state.Stages[t.Name]
	if r.Reusable(t) {
		return nil // idempotent: identical completed stage is not re-executed
	}
	st.Status = StatusRunning
	st.Attempts++
	st.InputSummary = t.InputSummary
	st.ParamSummary = t.ParamSummary
	if err := r.store.Save(r.state); err != nil {
		return err
	}
	guard := plan.NewGuard(t.Budget)
	ctx := &Context{Dir: r.store.Dir, Guard: guard, Stage: t.Name}
	start := time.Now()
	outputs, err := t.Run(ctx)
	elapsed := time.Since(start).Nanoseconds()
	guard.Add(elapsed, 0)
	if err != nil {
		st.Status = StatusFailed
		_ = r.store.Save(r.state)
		return err
	}
	if err := guard.Check(t.Name); err != nil {
		st.Status = StatusFailed
		_ = r.store.Save(r.state)
		return err
	}
	st.CPUNanos = guard.UsedCPU()
	st.BytesUsed = guard.UsedBytes()
	st.Outputs = outputs
	st.OutputSHA = map[string]string{}
	for _, rel := range outputs {
		sum, err := SHA256File(joinPath(r.store.Dir, rel))
		if err != nil {
			return err
		}
		st.OutputSHA[rel] = sum
	}
	st.Status = StatusCompleted
	if err := r.store.Save(r.state); err != nil {
		return err
	}
	if r.interrupt == t.Name {
		return ErrInterrupted
	}
	return nil
}

// ErrInterrupted signals a deliberate interruption after a stage boundary;
// state on disk is complete and resumable.
var ErrInterrupted = model.Code("ERR_INTERRUPTED").At("", 0, "stage", 0,
	"run interrupted after requested stage; state is resumable")

// IsInterrupted reports whether err is the controlled interruption sentinel.
func IsInterrupted(err error) bool {
	if err == nil {
		return false
	}
	return model.AsError(err).Code == model.Code("ERR_INTERRUPTED")
}

// VerifyReusable re-hashes every output of every completed stage and compares
// it with the persisted digest, returning ERR_MANIFEST_VERIFY on any drift.
func (r *Runner) VerifyReusable() error {
	for _, name := range r.state.Order {
		st := r.state.Stages[name]
		if st.Status != StatusCompleted {
			continue
		}
		for rel, want := range st.OutputSHA {
			got, err := SHA256File(joinPath(r.store.Dir, rel))
			if err != nil {
				return err
			}
			if got != want {
				return model.ErrManifestVerify.At(name, 0, "stage", 0,
					"output hash drift for "+rel)
			}
		}
	}
	return nil
}
