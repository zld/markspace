package txn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReplayDecision tells the pipeline what to do with one stage on recovery.
type ReplayDecision uint8

const (
	ReplaySkip   ReplayDecision = iota // committed and input hash matches
	ReplayRun                          // never finished or input changed
	ReplayRollback                     // partial artifacts from a crash
)

// RecoveryPlan is the per-stage replay map reconstructed from the journal.
type RecoveryPlan struct {
	Stages     map[string]ReplayDecision
	Boundary   string // last fully committed stage name, or "" if none
	Verified   []Entry
	Skipped    []string
	RolledBack []string
}

// ErrHashMismatch means a recorded stage input no longer matches the journal.
var ErrHashMismatch = errors.New("stage input hash differs from journal")

// Recover inspects the journal and current stage list, validates every
// committed stage's input hash, and cleans partial work from a crash.
// Only stages present in both the journal and the current plan with matching
// input hashes are eligible for replay-skip.
func (c *Coordinator) Recover(plan []Stage) (RecoveryPlan, error) {
	rp := RecoveryPlan{Stages: map[string]ReplayDecision{}}
	committed := map[string]Entry{}
	var order []string
	// First pass: collect every stage that reached a durable commit. A later
	// commit always supersedes earlier logged/prepared markers of the same
	// stage, so this pass must run before any rollback decisions are made.
	for _, e := range c.journal.Entries() {
		if e.State == StateCommitted {
			if _, ok := committed[e.Stage]; !ok {
				order = append(order, e.Stage)
			}
			committed[e.Stage] = e
		}
	}
	// Second pass: only uncommitted logged/prepared markers represent crash
	// leftovers that need their partial artifacts removed.
	for _, e := range c.journal.Entries() {
		if e.State == StatePrepared || e.State == StateLogged {
			if _, keep := committed[e.Stage]; keep {
				continue
			}
			if _, marked := rp.Stages[e.Stage]; marked {
				continue
			}
			rp.Stages[e.Stage] = ReplayRollback
			rp.RolledBack = append(rp.RolledBack, e.Stage)
			_ = os.RemoveAll(filepath.Join(c.tmpDir, filepath.Base(e.RelPath)))
			_ = os.RemoveAll(filepath.Join(c.outDir, filepath.Base(e.RelPath)))
		}
	}
	for seq, st := range plan {
		entry, wasCommitted := committed[st.Name]
		if !wasCommitted {
			if _, marked := rp.Stages[st.Name]; !marked {
				rp.Stages[st.Name] = ReplayRun
			}
			continue
		}
		if entry.InputHash != st.InputHash {
			// Input changed since the commit: cannot safely reuse it.
			return rp, fmt.Errorf("%w: stage %s journal=%s current=%s",
				ErrHashMismatch, st.Name, entry.InputHash, st.InputHash)
		}
		// Re-verify published artifacts still hash to the recorded value.
		target := c.stageOut(seq, st.Name)
		got, err := hashDir(target)
		if err != nil {
			rp.Stages[st.Name] = ReplayRollback
			rp.RolledBack = append(rp.RolledBack, st.Name)
			continue
		}
		if got != entry.OutputHash {
			return rp, fmt.Errorf("%w: stage %s artifacts changed on disk", ErrHashMismatch, st.Name)
		}
		rp.Stages[st.Name] = ReplaySkip
		rp.Skipped = append(rp.Skipped, st.Name)
		rp.Verified = append(rp.Verified, entry)
	}
	if len(order) > 0 {
		rp.Boundary = order[len(order)-1]
	}
	return rp, nil
}

// RunPlan executes a full ordered plan, skipping stages that recovery proved
// still valid and re-running everything at or after the first incomplete one.
// The first failure rolls back to the last complete commit boundary.
func (c *Coordinator) RunPlan(plan []Stage) (*RecoveryPlan, error) {
	rp, err := c.Recover(plan)
	if err != nil {
		return &rp, err
	}
	for seq, st := range plan {
		switch rp.Stages[st.Name] {
		case ReplaySkip:
			continue
		default:
			if _, err := c.Execute(seq, st); err != nil {
				if done := c.journal.SortedStages(); len(done) > 0 {
					rp.Boundary = done[len(done)-1]
				} else {
					rp.Boundary = ""
				}
				return &rp, err
			}
		}
	}
	full := c.journal.SortedStages()
	if len(full) > 0 {
		rp.Boundary = full[len(full)-1]
	}
	return &rp, nil
}

