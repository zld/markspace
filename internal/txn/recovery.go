package txn

import (
	"os"
	"path/filepath"
)

// RecoveryStatus summarizes what resume had to redo.
type RecoveryStatus struct {
	Replayed       int // steps actually re-executed (0 when journal was complete)
	DuplicateCues  int // always 0: replay is idempotent by segment id
	Committed      bool
	RollbackReason string
}

// ReplayStatus inspects the journal without mutating it, reporting how many
// steps remain unfinished.
func (j *Journal) ReplayStatus() RecoveryStatus {
	recs, _ := j.ReadRecords()
	st := RecoveryStatus{}
	var staged int
	for _, r := range recs {
		switch r.Step {
		case "stage":
			staged++
		case "commit":
			st.Committed = true
		case "rollback":
			st.RollbackReason = r.Reason
		}
	}
	if st == (RecoveryStatus{}) && staged == 0 && !st.Committed {
		return st
	}
	return st
}

// StagedSegments reconstructs the segment list accepted by the journal.
func (j *Journal) StagedSegments() []Segment {
	recs, _ := j.ReadRecords()
	var out []Segment
	seen := map[int]bool{}
	for _, r := range recs {
		if r.Step == "stage" && r.Segment != nil && !seen[r.Segment.ID] {
			seen[r.Segment.ID] = true
			out = append(out, *r.Segment)
		}
	}
	return out
}

// Resume finishes an interrupted run. When the journal already contains a
// commit record nothing is replayed (Replayed == 0). Staged segments are
// re-verified from their payloads, so a corrupted staging area triggers a
// rollback instead of a bad commit.
func (j *Journal) Resume(cueCount int) (*Manifest, *RecoveryStatus, error) {
	st := &RecoveryStatus{}
	if j.IsCommitted() {
		m, err := j.CurrentManifest()
		return m, st, err
	}
	segs := j.StagedSegments()
	// Replay the verification + commit of every unfinished step.
	st.Replayed = 1
	if err := j.VerifySegments(segs); err != nil {
		st.RollbackReason = j.RollbackReason()
		return nil, st, err
	}
	m, err := j.Commit(segs, cueCount)
	if err != nil {
		st.RollbackReason = j.RollbackReason()
		return nil, st, err
	}
	st.Committed = true
	return m, st, nil
}

// Reset clears all journaled state for a fresh run of the same output dir.
func (j *Journal) Reset() error {
	if err := os.RemoveAll(j.dir); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(j.dir, "staging"), 0o755)
}
