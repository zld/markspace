package task

import (
	"archivecheck/internal/merr"
)

// Options controls scheduling.
type Options struct {
	MaxAttempts int // >= 1; 1 means no retry
	// AlreadySucceeded marks keys whose artifacts are committed from a
	// previous run. Those nodes are skipped, enabling incremental
	// resume: only failed/invalidated nodes and their dependents rerun.
	AlreadySucceeded map[string]bool
}

// Run executes the DAG in dependency order. Nodes whose deps fail are
// marked blocked and never invoked; the first blocking dependency is
// reported with the original error's location.
func (d *DAG) Run(opts Options) *Result {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}
	res := &Result{States: map[string]*NodeState{}}
	for _, key := range d.order {
		res.States[key] = &NodeState{Key: key, State: StatusPending}
	}

	for _, key := range d.order {
		st := res.States[key]
		node := d.nodes[key]

		// Incremental resume: committed artifacts are trusted as-is.
		if opts.AlreadySucceeded[key] {
			st.State = StatusSuccess
			res.Succeeded++
			continue
		}

		blockedBy := d.firstFailedDep(key, res)
		if blockedBy != "" {
			st.State = StatusBlocked
			res.Skipped = append(res.Skipped, key)
			if res.Blocking == nil {
				res.Blocking = d.buildBlocking(key, blockedBy, res)
			}
			continue
		}

		st.State = StatusRunning
		var runErr error
		for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
			st.Attempts = attempt
			if err := node.Run(); err != nil {
				runErr = err
				continue
			}
			runErr = nil
			break
		}
		res.Executed++
		res.Order = append(res.Order, key)
		if runErr != nil {
			st.State = StatusFailed
			res.Failed++
			if me, ok := merr.As(runErr); ok {
				st.Err = me
			} else {
				st.Err = merr.Wrap(runErr, "task", "node:"+key, "node failed")
			}
			if res.Blocking == nil {
				res.Blocking = d.buildBlocking(key, key, res)
			}
		} else {
			st.State = StatusSuccess
			res.Succeeded++
		}
	}
	return res
}

// firstFailedDep returns the first dependency that is not successful,
// honoring the stable (sorted) dependency order for reproducible
// blocking reports.
func (d *DAG) firstFailedDep(key string, res *Result) string {
	deps := append([]string(nil), d.nodes[key].Deps...)
	sortStrings(deps)
	for _, dep := range deps {
		if res.States[dep].State != StatusSuccess {
			return dep
		}
	}
	return ""
}

func (d *DAG) buildBlocking(node, blockedBy string, res *Result) *BlockingDep {
	b := &BlockingDep{Node: node, BlockedBy: blockedBy}
	if st := res.States[blockedBy]; st != nil && st.Err != nil {
		b.Subsystem = st.Err.Subsystem
		b.Location = st.Err.Location
		b.Message = st.Err.Message
	} else {
		b.Subsystem = "task"
		b.Location = "node:" + blockedBy
		b.Message = "dependency did not complete successfully"
	}
	return b
}

// Invalidated returns the set of node keys that must rerun after the
// given keys failed: the failed keys themselves plus every transitive
// dependent. All other nodes stay committed and are skipped on resume.
func (d *DAG) Invalidated(failedKeys []string) map[string]bool {
	bad := map[string]bool{}
	for _, k := range failedKeys {
		bad[k] = true
	}
	changed := true
	for changed {
		changed = false
		for key, node := range d.nodes {
			if bad[key] {
				continue
			}
			for _, dep := range node.Deps {
				if bad[dep] {
					bad[key] = true
					changed = true
					break
				}
			}
		}
	}
	return bad
}

// ResumePlan converts an invalidated set into the AlreadySucceeded map
// for the next Run: everything not invalidated remains committed.
func (d *DAG) ResumePlan(invalidated map[string]bool) map[string]bool {
	committed := map[string]bool{}
	for key := range d.nodes {
		if !invalidated[key] {
			committed[key] = true
		}
	}
	return committed
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
