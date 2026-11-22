package journal

import (
	"fmt"
	"os"
	"path/filepath"

	"mediq/internal/timeline"
)

// StageState is the recovery verdict for one pipeline stage.
type StageState string

const (
	StateAbsent     StageState = "absent"
	StateCommitted  StageState = "committed"
	StateRolledBack StageState = "rolled_back"
	StateAborted    StageState = "aborted"
	StatePending    StageState = "pending" // prepared but never committed
)

// StageStatus summarizes one stage after WAL analysis.
type StageStatus struct {
	Stage      string
	State      StageState
	OutputHash string
	Files      []string
}

// Report is the recovery analysis of a workspace journal.
type Report struct {
	Stages       []StageStatus
	Replayed     int // committed records re-anchored on restart
	RolledBack   int // pending stages whose temp files were removed
	PendingFiles []string
}

// CommittedStages returns stage names in commit order.
func (r *Report) CommittedStages() []string {
	var out []string
	for _, s := range r.Stages {
		if s.State == StateCommitted {
			out = append(out, s.Stage)
		}
	}
	return out
}

// IsCommitted reports whether a stage reached a durable commit.
func (r *Report) IsCommitted(stage string) bool {
	for _, s := range r.Stages {
		if s.Stage == stage {
			return s.State == StateCommitted
		}
	}
	return false
}

// Analyze reads the WAL and derives one verdict per stage. The latest
// terminal record (committed / rolled_back / aborted) wins; a stage
// whose last record is begin/prepared is pending and must be rolled
// back before a safe restart.
func Analyze(workDir string) (*Report, error) {
	records, err := ReadAll(workDirPath(workDir))
	if err != nil {
		return nil, timeline.New(timeline.StageRecover, timeline.KindInconsistent, -1,
			"abort_recover", err.Error())
	}
	order := []string{}
	seen := map[string]int{}
	status := map[string]*StageStatus{}
	for _, rec := range records {
		idx, ok := seen[rec.Stage]
		if !ok {
			idx = len(order)
			seen[rec.Stage] = idx
			order = append(order, rec.Stage)
			status[rec.Stage] = &StageStatus{Stage: rec.Stage, State: StateAbsent}
		}
		st := status[rec.Stage]
		switch rec.Kind {
		case KindBegin:
			if st.State != StateCommitted {
				st.State = StatePending
			}
		case KindPrepared:
			if st.State != StateCommitted {
				st.State = StatePending
			}
			// A stage can prepare many temp files across multiple
			// records; accumulate all of them so rollback misses none.
			st.Files = append(st.Files, rec.Files...)
		case KindCommitted:
			st.State = StateCommitted
			st.OutputHash = rec.OutputHash
			if len(rec.Files) > 0 {
				st.Files = rec.Files
			}
		case KindRolledBack:
			st.State = StateRolledBack
			st.Files = rec.Files
		case KindAborted:
			st.State = StateAborted
		}
		_ = idx
	}
	rep := &Report{}
	for _, name := range order {
		st := status[name]
		rep.Stages = append(rep.Stages, *st)
		switch st.State {
		case StateCommitted:
			rep.Replayed++
		case StatePending:
			rep.PendingFiles = append(rep.PendingFiles, st.Files...)
		}
	}
	return rep, nil
}

// Recover analyzes the journal, removes every pending temporary file
// and appends rollback records so the next run starts from the last
// durable commit. Already committed stages are never touched. It is
// safe to call when no journal exists.
func Recover(workDir string) (*Report, error) {
	rep, err := Analyze(workDir)
	if err != nil {
		return nil, err
	}
	w, err := Open(workDir)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	for _, st := range rep.Stages {
		if st.State != StatePending {
			continue
		}
		var removed []string
		for _, f := range st.Files {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				return nil, timeline.New(timeline.StageRecover, timeline.KindIO, -1,
					"abort_recover", fmt.Sprintf("remove %s: %v", f, err))
			}
			if err == nil {
				removed = append(removed, f)
			}
		}
		if _, err := w.RolledBack(st.Stage, removed); err != nil {
			return nil, err
		}
		rep.RolledBack++
	}
	rep.PendingFiles = nil
	return rep, nil
}

// VerifyCommitHash checks that a committed output still hashes to the
// value recorded in the WAL. A mismatch means the commit cannot be
// trusted and must be surfaced as a recovery failure.
func VerifyCommitHash(workDir, stage string, hashOf func(path string) (string, error)) error {
	rep, err := Analyze(workDir)
	if err != nil {
		return err
	}
	for _, st := range rep.Stages {
		if st.Stage != stage || st.State != StateCommitted {
			continue
		}
		for _, f := range st.Files {
			got, err := hashOf(f)
			if err != nil {
				return timeline.New(timeline.StageRecover, timeline.KindIO, -1,
					"reject_commit", fmt.Sprintf("rehash %s: %v", f, err))
			}
			if got != st.OutputHash {
				return timeline.New(timeline.StageRecover, timeline.KindInconsistent, -1,
					"reject_commit", fmt.Sprintf("stage %s file %s hash drifted", stage, f))
			}
		}
		return nil
	}
	return timeline.New(timeline.StageRecover, timeline.KindInconsistent, -1,
		"reject_commit", fmt.Sprintf("no committed record for stage %s", stage))
}

func workDirPath(workDir string) string {
	return filepath.Join(workDir, DirName, WALName)
}
