// Package rollback: classifying an interrupted commit.
package rollback

import (
	"fmt"
	"os"

	"example.com/assetpack/internal/manifest"
)

// Recover classifies an interrupted commit.  It never mutates the manifest
// unless the journal says the process died mid-commit, in which case the
// staged-but-unrenamed release is discarded and the previous one is kept.
func (c *Checkpoint) Recover() (*RecoveryState, error) {
	j, err := c.ReadJournal()
	if err != nil {
		return nil, err
	}
	cur, err := c.ReadManifest()
	if err != nil {
		return nil, err
	}
	st := &RecoveryState{}
	if cur != nil {
		st.Digest = cur.Digest
		st.Release = cur.Release
	}
	if j == nil {
		if cur == nil {
			st.OldOrNew = "empty"
			st.Detail = "no manifest and no journal"
			_ = c.pruneArtifacts()
			return st, nil
		}
		st.OldOrNew = "new"
		st.Detail = "committed manifest present, no journal"
		_ = c.pruneArtifacts()
		return st, nil
	}
	switch j.Phase {
	case PhaseCommited:
		if cur != nil && cur.Digest == j.NewDigest {
			st.OldOrNew = "new"
			st.Detail = "journal committed and manifest matches"
			_ = os.RemoveAll(c.path(j.Staging))
			_ = os.Remove(c.path(RollbackName))
			return st, nil
		}
		st.Mixed = true
		st.Detail = fmt.Sprintf("journal says committed but manifest is %q", digestOf(cur))
		return st, nil
	case PhaseRolledBack:
		if cur != nil && cur.Digest == j.OldDigest {
			st.OldOrNew = "old"
			st.Detail = "journal rolled back and previous manifest restored"
			return st, nil
		}
		if cur == nil && j.OldDigest == "" {
			st.OldOrNew = "empty"
			st.Detail = "journal rolled back to an empty release"
			return st, nil
		}
		st.Mixed = true
		st.Detail = "journal says rolled back but the manifest does not match"
		return st, nil
	default: // staged: interrupted before or during the commit rename
		switch {
		case j.OldDigest == "" && cur == nil:
			// The rename never happened (or it happened and then the old
			// manifest was correctly absent).  Discard the stage.
			_ = os.RemoveAll(c.path(j.Staging))
			_ = os.Remove(c.path(JournalName))
			st.OldOrNew = "empty"
			st.Detail = "interrupted before commit; staged release discarded"
			_ = c.pruneArtifacts()
			return st, nil
		case cur != nil && cur.Digest == j.NewDigest:
			// Rename happened but the journal was not updated.
			j.Phase = PhaseCommited
			_ = c.writeJournal(j)
			_ = os.RemoveAll(c.path(j.Staging))
			st.OldOrNew = "new"
			st.Detail = "commit rename completed; journal repaired"
			return st, nil
		case cur != nil && cur.Digest == j.OldDigest:
			_ = os.RemoveAll(c.path(j.Staging))
			_ = os.Remove(c.path(JournalName))
			st.OldOrNew = "old"
			st.Detail = "interrupted before commit; previous release intact"
			_ = c.pruneArtifacts()
			return st, nil
		case cur == nil && j.OldDigest != "":
			j.Phase = PhaseRolledBack
			_ = c.writeJournal(j)
			st.OldOrNew = "empty"
			st.Detail = "interrupted during commit and no manifest is present"
			return st, nil
		default:
			st.Mixed = true
			st.Detail = fmt.Sprintf("manifest digest %q matches neither old %q nor new %q",
				digestOf(cur), j.OldDigest, j.NewDigest)
			return st, nil
		}
	}
}

func digestOf(m *manifest.V2) string {
	if m == nil {
		return ""
	}
	return m.Digest
}
