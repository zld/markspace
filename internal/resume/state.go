// Package resume validates on-disk checkpoint state and drives a continuation
// run that reuses committed blocks. It deliberately keeps the persistence
// mechanics in index.Store and adds the policy the runner and command rely on:
// refuse to resume a changed or damaged input, skip blocks that still verify,
// and redo every block that was never atomically committed.
package resume

import (
	"fmt"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/index"
)

// State is the validated view of a checkpoint directory.
type State struct {
	fp        index.Fingerprint
	store     *index.Store
	committed map[uint64]bool
	entries   []index.Entry
	// MaxSeq is the highest validated sequence, or -1 for a fresh work dir.
	MaxSeq int64
}

// Load validates the working directory against fp and returns its state. A
// damaged checkpoint or an input whose fingerprint differs is reported with
// ErrResumeMismatch so the caller can pick the dedicated exit code rather than
// silently rebuilding over foreign data.
func Load(dir string, fp index.Fingerprint) (*State, error) {
	store, err := index.NewStore(dir, fp)
	if err != nil {
		return nil, clock.Wrap(clock.StageResume, "open-store", err)
	}
	rec, err := store.Recover()
	if err != nil {
		return nil, clock.Wrap(clock.StageResume, "validate", err)
	}
	st := &State{
		fp:        fp,
		store:     store,
		committed: map[uint64]bool{},
		MaxSeq:    -1,
	}
	for _, seq := range rec.Committed {
		st.committed[seq] = true
		if int64(seq) > st.MaxSeq {
			st.MaxSeq = int64(seq)
		}
	}
	st.entries = rec.Entries
	return st, nil
}

// Store exposes the underlying checkpoint store for writing new blocks.
func (s *State) Store() *index.Store { return s.store }

// Fingerprint returns the input digest the state was validated against.
func (s *State) Fingerprint() index.Fingerprint { return s.fp }

// Resumed reports whether a continuation run found reusable work.
func (s *State) Resumed() bool { return len(s.committed) > 0 }

// ResumedBlocks counts validated, skippable blocks.
func (s *State) ResumedBlocks() int { return len(s.committed) }

// ShouldSkip reports whether a block sequence is already durably committed.
func (s *State) ShouldSkip(seq uint64) bool { return s.committed[seq] }

// CommittedEntries returns the decoded rows of every validated block.
func (s *State) CommittedEntries() []index.Entry {
	out := make([]index.Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// EnsureContiguous verifies that committed sequences form 0..n with no hole. A
// hole means a block was lost even though a later one exists, which the chunk
// naming alone cannot prove safe to resume. A fresh, empty state is contiguous.
func (s *State) EnsureContiguous() error {
	if s.MaxSeq < 0 {
		return nil
	}
	for seq := uint64(0); seq <= uint64(s.MaxSeq); seq++ {
		if !s.committed[seq] {
			return clock.NewError(clock.StageResume, "contiguous",
				fmt.Errorf("%w: missing block %d before block %d",
					clock.ErrResumeMismatch, seq, s.MaxSeq))
		}
	}
	return nil
}
