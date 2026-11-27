// Package recovery decides where an interrupted or failed run resumes and
// what must be rolled back. It reads only the write-ahead log and the
// transaction directory; it never reprocesses bitstream payloads.
package recovery

import (
	"os"
	"path/filepath"

	perr "gamepack/internal/errors"
	"gamepack/internal/txn"
)

const subsystem = "recovery"

// Plan describes the resume decision for one job directory.
type Plan struct {
	// ResumeStage is the first stage to (re)run.
	ResumeStage txn.Stage
	// VerifiedStage is the highest durable, verified stage already complete.
	VerifiedStage txn.Stage
	ResumeAt      string
	Rollbacks     int
	Leftovers     int
	// Fresh is true when no prior WAL exists (a brand-new job).
	Fresh bool
}

// Inspect reads the WAL and transaction directory to produce a resume plan.
// It also cleans recognizable crash leftovers and counts them.
func Inspect(dir string) (*Plan, error) {
	wal, err := txn.Open(dir)
	if err != nil {
		return nil, err
	}
	leftovers, err := txn.CleanLeftovers(dir)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Rollbacks: wal.RollbackCount(), Leftovers: leftovers}

	records := wal.Records()
	if len(records) == 0 {
		plan.Fresh = true
		plan.ResumeStage = txn.StageScan
		plan.ResumeAt = "0"
		return plan, nil
	}

	last := records[len(records)-1]
	verifiedStage, rec := wal.LastVerified()
	if rec != nil {
		plan.VerifiedStage = verifiedStage
		plan.ResumeAt = rec.ResumeAt
	}

	switch {
	case last.State == txn.StateFailed || last.State == txn.StateRolledBack:
		// After a failure/rollback, restart the stage that failed.
		plan.ResumeStage = last.Stage
	case verifiedStage == "":
		// Nothing verified yet: restart from the very first stage.
		plan.ResumeStage = txn.StageScan
		plan.ResumeAt = "0"
	case verifiedStage == txn.StageCommit:
		// The whole job already committed; nothing left to do.
		plan.ResumeStage = txn.StageCommit
	default:
		// Resume at the stage immediately after the last verified one.
		plan.ResumeStage = nextStage(verifiedStage)
	}
	return plan, nil
}

// nextStage returns the stage following s in the ordered pipeline.
func nextStage(s txn.Stage) txn.Stage {
	idx := -1
	for i, st := range txn.OrderedStages {
		if st == s {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(txn.OrderedStages) {
		return txn.StageCommit
	}
	return txn.OrderedStages[idx+1]
}

// MarkRollback records a rollback in the WAL for the given failed stage and
// returns the updated count. Outputs created after the last commit point are
// removed by the caller through the segment directory.
func MarkRollback(w *txn.WAL, stage txn.Stage, resumeAt string) error {
	_, err := w.Append(txn.Record{
		Stage: stage, State: txn.StateRolledBack, ResumeAt: resumeAt,
	})
	if err != nil {
		return err
	}
	return nil
}

// RemoveStageOutputs deletes fragment files under segDir for a rolled-back
// stage, leaving the WAL and committed manifest untouched.
func RemoveStageOutputs(segDir string) error {
	if _, err := os.Stat(segDir); os.IsNotExist(err) {
		return nil
	}
	entries, err := os.ReadDir(segDir)
	if err != nil {
		return perr.New(subsystem, perr.CodeTxn, "read segment dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(segDir, e.Name())); err != nil {
			return perr.New(subsystem, perr.CodeTxn,
				"remove rolled-back output %s: %v", e.Name(), err)
		}
	}
	return nil
}
