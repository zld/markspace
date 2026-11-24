// Package journal records pipeline stage state so interrupted runs can
// resume: each stage is keyed by the digest of its inputs, the parameter
// digest and the stage version, and only stages whose key still matches
// the current inputs are reused. Changing any input byte invalidates the
// matching stages.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"mediarun/internal/fault"
)

// FileName is the journal file inside the output directory.
const FileName = "journal.json"

// StageStatus is the lifecycle state of a stage record.
type StageStatus string

const (
	StatusVerified  StageStatus = "verified"  // output written and digest-checked
	StatusCommitted StageStatus = "committed" // part of a committed final output
)

// StageRecord is one stage's resume record.
type StageRecord struct {
	Name         string      `json:"name"`
	Version      int         `json:"version"`
	Key          string      `json:"key"`
	Status       StageStatus `json:"status"`
	OutputDigest string      `json:"outputDigest"` // sha256 of the stage output file
	Reused       bool        `json:"-"`            // set when reused this run
}

// Journal is the resume log of one output directory.
type Journal struct {
	InputDigest string         `json:"inputDigest"`
	ParamDigest string         `json:"paramDigest"`
	Stages      []*StageRecord `json:"stages"`
}

// StageKey derives the idempotency key of a stage from the input digest,
// the parameter digest and the stage version.
func StageKey(inputDigest, paramDigest, stage string, version int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d", inputDigest, paramDigest, stage, version)
	return hex.EncodeToString(h.Sum(nil))
}

// DigestBytes hashes raw bytes to a hex digest.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Load reads the journal from dir. A missing journal yields an empty one;
// a corrupt journal is a resume failure (exit code 3 via StageJournal).
func Load(dir string) (*Journal, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Journal{}, nil
		}
		return nil, fault.New(fault.StageJournal, "cannot read journal").At(dir, -1).Wrap(err)
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fault.New(fault.StageJournal, "corrupt journal").At(dir, -1).Wrap(err)
	}
	return &j, nil
}

// Lookup returns the record for stage if its key matches and it reached
// at least verified state.
func (j *Journal) Lookup(stage, key string) (*StageRecord, bool) {
	for _, r := range j.Stages {
		if r.Name == stage && r.Key == key &&
			(r.Status == StatusVerified || r.Status == StatusCommitted) {
			return r, true
		}
	}
	return nil, false
}

// Put inserts or replaces a stage record.
func (j *Journal) Put(rec *StageRecord) {
	for i, r := range j.Stages {
		if r.Name == rec.Name {
			j.Stages[i] = rec
			return
		}
	}
	j.Stages = append(j.Stages, rec)
}

// MarkCommitted moves all verified stages to committed state.
func (j *Journal) MarkCommitted() {
	for _, r := range j.Stages {
		if r.Status == StatusVerified {
			r.Status = StatusCommitted
		}
	}
}

// ReusedCount reports how many stages were reused this run.
func (j *Journal) ReusedCount() int {
	n := 0
	for _, r := range j.Stages {
		if r.Reused {
			n++
		}
	}
	return n
}

// Save writes the journal atomically (temp file + rename) inside dir.
func (j *Journal) Save(dir string) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fault.New(fault.StageJournal, "cannot encode journal").Wrap(err)
	}
	final := filepath.Join(dir, FileName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fault.New(fault.StageJournal, "cannot write journal").At(tmp, -1).Wrap(err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fault.New(fault.StageJournal, "cannot commit journal").At(final, -1).Wrap(err)
	}
	return nil
}
