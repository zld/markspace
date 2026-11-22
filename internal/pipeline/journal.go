// Package pipeline coordinates parse, transform, render and verify
// stages with journaled checkpoints so an interrupted run can resume
// without reusing stale outputs.
package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StageOrder is the execution order of journaled stages.
var StageOrder = []string{"parse", "compose", "render"}

// StageCheckpoint binds a stage's output to the input hash and the
// policy version that produced it.
type StageCheckpoint struct {
	InputHash     string `json:"input_hash"`
	PolicyVersion string `json:"policy_version"`
	Digest        string `json:"digest"` // digest of the stage output artifact
	Done          bool   `json:"done"`
}

// Journal is the on-disk resume state.
type Journal struct {
	Stages map[string]*StageCheckpoint `json:"stages"`
}

func journalPath(dir string) string { return filepath.Join(dir, "journal.json") }

// LoadJournal reads the journal; a missing journal yields an empty one.
func LoadJournal(dir string) (*Journal, error) {
	data, err := os.ReadFile(journalPath(dir))
	if os.IsNotExist(err) {
		return &Journal{Stages: map[string]*StageCheckpoint{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		// A corrupt journal is not trusted: start over.
		return &Journal{Stages: map[string]*StageCheckpoint{}}, nil
	}
	if j.Stages == nil {
		j.Stages = map[string]*StageCheckpoint{}
	}
	return &j, nil
}

// Save atomically replaces the journal file.
func (j *Journal) Save(dir string) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(journalPath(dir), data)
}

// ValidStage returns the checkpoint if it is complete and bound to the
// same input hash and policy version.
func (j *Journal) ValidStage(stage, inputHash, policyVersion string) (*StageCheckpoint, bool) {
	cp, ok := j.Stages[stage]
	if !ok || !cp.Done {
		return nil, false
	}
	if cp.InputHash != inputHash || cp.PolicyVersion != policyVersion {
		return nil, false
	}
	return cp, true
}

// Record stores a stage checkpoint and drops every later stage: once a
// stage is recomputed, downstream checkpoints are stale.
func (j *Journal) Record(stage, inputHash, policyVersion, digest string) {
	j.Stages[stage] = &StageCheckpoint{
		InputHash: inputHash, PolicyVersion: policyVersion, Digest: digest, Done: true,
	}
	for i, s := range StageOrder {
		if s == stage {
			for _, later := range StageOrder[i+1:] {
				delete(j.Stages, later)
			}
			return
		}
	}
}

// atomicWrite writes data to a temporary file in the same directory
// and renames it over the target.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
