package recovery

import (
	"fmt"
	"os"
	"path/filepath"

	"regress/internal/fault"
	"regress/internal/worker"
)

// ClaimResult reports what a resume run did with an existing job.
type ClaimResult struct {
	JobKey         string
	Claimed        bool
	Recovered      int // jobs claimed and state-completed (0 or 1)
	SegmentsOK     int // segments whose on-disk hash matched the journal
	Published      int // segments newly published (zero on a pure resume)
	Duplicate      int // duplicate republish attempts (zero on a clean resume)
	Conflict       bool
	RejectedReason string
}

// HashProvider abstracts file hashing so tests can inject in-memory content
// without touching disk; worker.HashFile is the production implementation.
type HashProvider func(path string) (string, error)

// Resume attempts to claim an already-published job. It verifies that:
//
//   - the journal knows the job key and the config hash matches;
//   - every recorded segment still exists under pubRoot and hashes identically.
//
// On success the state is advanced to committed without rewriting any product
// (Published stays zero). Any content mismatch is a refused overwrite with a
// conflict code and the existing products are left untouched.
func (j *Journal) Resume(jobKey, configHash, pubRoot string, nowNano int64, hash HashProvider) (*ClaimResult, error) {
	j.mu.Lock()
	rec, ok := j.records[jobKey]
	j.mu.Unlock()
	res := &ClaimResult{JobKey: jobKey}
	if !ok {
		return nil, fault.New(fault.StageRecovery, fault.CodeStateConflict, "",
			"no journal record for job key: "+jobKey)
	}
	if rec.ConfigHash != configHash {
		res.Conflict = true
		res.RejectedReason = "config hash mismatch; refusing to overwrite existing products"
		return res, fault.New(fault.StageRecovery, fault.CodeContentConflict, "",
			fmt.Sprintf("job %s config hash: journal=%s request=%s",
				jobKey, rec.ConfigHash, configHash))
	}
	if hash == nil {
		hash = worker.HashFile
	}
	for _, seg := range rec.Segments {
		path := filepath.Join(pubRoot, jobKey, seg.File)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			res.Conflict = true
			res.RejectedReason = "recorded segment missing on disk: " + seg.File
			return res, fault.New(fault.StageRecovery, fault.CodeContentConflict, "",
				"segment vanished: "+seg.File).Wrap(err)
		}
		got, err := hash(path)
		if err != nil {
			return res, err
		}
		if got != seg.SHA256 {
			res.Conflict = true
			res.RejectedReason = "segment content hash mismatch: " + seg.File
			return res, fault.At(fault.StageRecovery, fault.CodeContentConflict, "",
				int64(info.Size()),
				fmt.Sprintf("journal=%s actual=%s file=%s", seg.SHA256, got, seg.File))
		}
		res.SegmentsOK++
	}
	// All products verified: only the state is recorded now. The job itself is
	// the unit of recovery, so Recovered counts the single claimed job; no
	// product is rewritten, hence Published and Duplicate stay zero.
	if _, err := j.Transition(jobKey, StateCommitted, nowNano); err != nil {
		return res, err
	}
	res.Claimed = true
	res.Recovered = 1
	return res, nil
}

// NeedsRepublish reports whether a journal state requires the write path to
// run again (anything before published) versus a pure state-completion resume.
func NeedsRepublish(rec *Record) bool {
	if rec == nil {
		return true
	}
	return rec.State != StatePublished && rec.State != StateCommitted
}
