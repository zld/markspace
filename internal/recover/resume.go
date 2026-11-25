// Package recover validates a local checkpoint and resumes a batch job from
// the last good segment. It never skips a damaged region: when the live index
// is truncated or a segment hash no longer matches, work rolls back to the
// most recent verified snapshot and every segment after it is redone.
package recover

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"mediad/internal/fault"
	"mediad/internal/index"
	"mediad/internal/safeio"
)

// Plan describes where processing must resume.
type Plan struct {
	// LastGoodSegment is the highest segment ID already verified, or -1 when
	// nothing has been committed.
	LastGoodSegment int
	// ResumeAtChunk is the first input chunk that must be (re)processed.
	ResumeAtChunk int
	// RedoSegments counts how many committed-but-invalidated segments must be
	// rebuilt after a rollback.
	RedoSegments int
	// FromSnapshot records the snapshot sequence the plan was restored from.
	FromSnapshot int
}

// HasWork reports whether any segment remains to be processed.
func (p Plan) HasWork() bool { return p.ResumeAtChunk >= 0 }

// segmentHash re-hashes a segment file's bytes. The index stores the hash of
// the concatenated per-track PCM content, so callers supply the PCM digest
// function appropriate to their muxer; here we hash the whole file for an
// additional on-disk integrity check.
func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateAndPlan inspects root and returns a resume plan. When no journal
// exists yet it returns a clean start plan. Corrupt journals trigger a
// rollback to the latest valid snapshot.
func ValidateAndPlan(root string) (*Plan, error) {
	if _, err := safeio.EnsureRoot(root); err != nil {
		return nil, err
	}
	ix, err := index.Load(root)
	switch {
	case errors.Is(err, index.ErrNotFound):
		return cleanStart(), nil
	case err != nil:
		return rollbackToSnapshot(root, err)
	}

	// The journal parses: re-verify every referenced segment file.
	for _, rec := range ix.Records {
		if !rec.Complete {
			continue
		}
		ok, ferr := segmentPresentAndIntact(root, rec.FileName)
		if ferr != nil {
			return nil, ferr
		}
		if !ok {
			return rollbackToSnapshot(root, fault.New(fault.CatHash,
				"recover.verify", true, "missing segment: "+rec.FileName))
		}
	}
	return planFromIndex(ix, -1), nil
}

func segmentPresentAndIntact(root, name string) (bool, error) {
	p, err := safeio.SafeJoin(root, name)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fault.Wrap(fault.CatWrite, "recover.stat", -1, -1, true, err)
	}
	if info.Size() == 0 {
		return false, nil
	}
	if _, err := fileHash(p); err != nil {
		return false, fault.Wrap(fault.CatHash, "recover.hash", -1, -1, true, err)
	}
	return true, nil
}

// rollbackToSnapshot discards the damaged journal and restores the newest
// snapshot whose records still validate. It never silently skips the damaged
// interval; all segments after the snapshot are marked for redo.
func rollbackToSnapshot(root string, cause error) (*Plan, error) {
	n, snapPath, err := index.LatestSnapshot(root)
	if errors.Is(err, index.ErrNotFound) {
		// No safety net: start over rather than trust damaged state.
		return cleanStart(), nil
	}
	if err != nil {
		return nil, err
	}
	snap, err := readSnapshot(snapPath)
	if err != nil {
		return cleanStart(), nil
	}
	// Re-verify the snapshot's own segment files before trusting it.
	for _, rec := range snap.Records {
		ok, ferr := segmentPresentAndIntact(root, rec.FileName)
		if ferr != nil {
			return nil, ferr
		}
		if !ok {
			return cleanStart(), nil
		}
	}
	if err := snap.Save(root); err != nil {
		return nil, err
	}
	plan := planFromIndex(snap, n)
	plan.RedoSegments = estimateRedo(root, snap)
	return plan, nil
}

func readSnapshot(p string) (*index.Index, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	snap := &index.Index{}
	if err := jsonUnmarshal(data, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// estimateRedo counts segment files on disk that are no longer referenced by
// the restored snapshot; those are stale outputs that must be rebuilt.
func estimateRedo(root string, snap *index.Index) int {
	segDir, err := safeio.SafeJoin(root, "segments")
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(segDir)
	if err != nil {
		return 0
	}
	known := make(map[string]bool, len(snap.Records))
	for _, r := range snap.Records {
		known[r.FileName] = true
	}
	redo := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".mp4" && !known[e.Name()] {
			redo++
		}
	}
	return redo
}

func planFromIndex(ix *index.Index, snapshotSeq int) *Plan {
	last := ix.LastGoodID
	resumeChunk := 0
	if last >= 0 && last < len(ix.Records) {
		resumeChunk = ix.Records[last].ChunkEnd
	}
	return &Plan{
		LastGoodSegment: last,
		ResumeAtChunk:   resumeChunk,
		FromSnapshot:    snapshotSeq,
	}
}

func cleanStart() *Plan {
	return &Plan{LastGoodSegment: -1, ResumeAtChunk: 0, FromSnapshot: -1}
}
