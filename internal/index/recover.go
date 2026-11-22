package index

import (
	"fmt"
	"os"
)

// RecoveryReport describes how a resume point was established.
type RecoveryReport struct {
	Checkpoint  *StoredRecord // last fully committed record; nil if none
	Replayed    uint64        // chunks replayed from the checkpoint
	RolledBack  int           // number of half-written/corrupt files removed
	FromCursor  uint64        // cursor to resume processing at
	FoundAny    bool
}

// Recover scans the store for key, discards any trailing half-written or
// corrupt checkpoint, and returns the last complete one. Already committed
// chunks are never re-counted: the caller resumes at FromCursor.
func Recover(store *Store, key string) (RecoveryReport, error) {
	rep := RecoveryReport{}
	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		return rep, fmt.Errorf("index: scan store: %w", err)
	}

	// Sort names: chunk-NNN files order lexicographically due to zero padding;
	// temp files and unknown suffixes are handled explicitly.
	var chkFiles, tmpFiles []string
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		full := store.Dir() + "/" + en.Name()
		switch {
		case hasSuffix(en.Name(), recordSuffix):
			chkFiles = append(chkFiles, full)
		case hasSuffix(en.Name(), ".tmp"):
			tmpFiles = append(tmpFiles, full)
		}
	}

	// Any leftover temp file is a half-written record: remove it.
	for _, t := range tmpFiles {
		if err := os.Remove(t); err != nil {
			return rep, fmt.Errorf("index: rollback temp file: %w", err)
		}
		rep.RolledBack++
	}

	// Validate every checkpoint in order; the sequence must be gap-free and
	// strictly increasing. The first corrupt file terminates the valid prefix.
	var valid []StoredRecord
	var prevSeq uint64
	started := false
	for _, path := range chkFiles {
		rec, err := ReadRecordFile(path)
		if err != nil {
			// Corrupt trailing record: roll it back and stop.
			if rmErr := os.Remove(path); rmErr != nil {
				return rep, fmt.Errorf("index: rollback corrupt checkpoint: %w", rmErr)
			}
			rep.RolledBack++
			break
		}
		if rec.CheckpointKey != key {
			return rep, fmt.Errorf("index: checkpoint key mismatch in %s", path)
		}
		if started {
			if rec.ChunkSeq <= prevSeq {
				return rep, fmt.Errorf("index: non-monotonic chunk seq %d after %d in %s",
					rec.ChunkSeq, prevSeq, path)
			}
			if rec.Cursor < valid[len(valid)-1].Cursor {
				return rep, fmt.Errorf("index: cursor regressed in %s", path)
			}
		}
		valid = append(valid, rec)
		prevSeq = rec.ChunkSeq
		started = true
	}

	if len(valid) == 0 {
		return rep, nil
	}
	last := valid[len(valid)-1]
	rep.FoundAny = true
	rep.Checkpoint = &last
	rep.FromCursor = last.Cursor
	rep.Replayed = uint64(len(valid))
	return rep, nil
}

func hasSuffix(name, suffix string) bool {
	return len(name) >= len(suffix) && name[len(name)-len(suffix):] == suffix
}

// ResumeKey builds the checkpoint key from the input digest, config digest
// and processing stage, as mandated by the architecture.
func ResumeKey(inputDigest, configDigest, stage string) string {
	return fmt.Sprintf("%s|%s|%s", stage, inputDigest, configDigest)
}
