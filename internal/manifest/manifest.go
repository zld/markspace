// Package manifest builds the deterministic run manifest.
//
// The manifest records, per stage, the digest of its input, the digest of its
// parameters and its completion status, plus the digest of every artifact it
// produced. It is written as canonical JSON: struct fields are emitted in a
// fixed order, slices keep insertion order and no map is serialised directly,
// so identical inputs always produce byte-identical manifests and hashes.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Version is the manifest schema version.
const Version = 1

// Status is a stage status.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusPartial Status = "partial"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// StageRecord is one stage's contribution.
type StageRecord struct {
	Stage        string `json:"stage"`
	Status       Status `json:"status"`
	InputSHA256  string `json:"input_sha256"`
	ParamSHA256  string `json:"param_sha256"`
	OutputSHA256 string `json:"output_sha256"`
	// Note carries a short deterministic explanation (counts, first error).
	Note string `json:"note,omitempty"`
}

// Artifact describes one published file.
type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is the whole run record.
type Manifest struct {
	SchemaVersion int           `json:"schema_version"`
	Seed          int64         `json:"seed"`
	BudgetMS      int64         `json:"budget_ms"`
	StreamSHA256  string        `json:"stream_sha256"`
	StartUnixNano int64         `json:"start_unix_nano"`
	Stages        []StageRecord `json:"stages"`
	Artifacts     []Artifact    `json:"artifacts"`
	// ResumeStage is the first stage that is not complete.
	ResumeStage string `json:"resume_stage"`
}

// New creates a manifest with the stages pre-declared in pipeline order and
// every stage marked pending. Declaring them up front keeps the manifest shape
// identical whether a run finishes or is interrupted early.
func New(seed int64, budgetMS int64, startUnixNano int64, stages []string) *Manifest {
	m := &Manifest{
		SchemaVersion: Version,
		Seed:          seed,
		BudgetMS:      budgetMS,
		StartUnixNano: startUnixNano,
		ResumeStage:   "complete",
	}
	for _, s := range stages {
		m.Stages = append(m.Stages, StageRecord{Stage: s, Status: StatusPending})
	}
	return m
}

// SetStageInput records the input and parameter digests for a stage.
func (m *Manifest) SetStageInput(stage, inputSHA, paramSHA string) {
	if s := m.find(stage); s != nil {
		s.InputSHA256 = inputSHA
		s.ParamSHA256 = paramSHA
	}
}

// SetStage records a stage outcome.
func (m *Manifest) SetStage(stage string, status Status, outputSHA, note string) {
	if s := m.find(stage); s != nil {
		s.Status = status
		s.OutputSHA256 = outputSHA
		s.Note = note
	}
}

func (m *Manifest) find(stage string) *StageRecord {
	for i := range m.Stages {
		if m.Stages[i].Stage == stage {
			return &m.Stages[i]
		}
	}
	return nil
}

// StageStatus returns the recorded status for a stage.
func (m *Manifest) StageStatus(stage string) (Status, bool) {
	if s := m.find(stage); s != nil {
		return s.Status, true
	}
	return "", false
}

// AddArtifact appends an artifact record. Artifacts are kept in insertion
// order; callers add them deterministically.
func (m *Manifest) AddArtifact(name, sha string, size int64) {
	m.Artifacts = append(m.Artifacts, Artifact{Name: name, Size: size, SHA256: sha})
}

// SortArtifacts orders artifacts by name so manifest bytes do not depend on
// publication order.
func (m *Manifest) SortArtifacts() {
	sort.SliceStable(m.Artifacts, func(i, j int) bool { return m.Artifacts[i].Name < m.Artifacts[j].Name })
}

// ComputeResumeStage sets ResumeStage to the first stage that did not finish,
// or "complete" when every stage finished.
//
// A partial stage did finish its work while recording recoverable damage, so it
// counts as finished for resume purposes; replaying it would not change its
// output.
func (m *Manifest) ComputeResumeStage() {
	for _, s := range m.Stages {
		if s.Status != StatusDone && s.Status != StatusPartial && s.Status != StatusSkipped {
			m.ResumeStage = s.Stage
			return
		}
	}
	m.ResumeStage = "complete"
}

// Bytes returns the canonical encoding.
func (m *Manifest) Bytes() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// SHA256 returns the hex digest of the canonical encoding.
func (m *Manifest) SHA256() (string, error) {
	b, err := m.Bytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Parse decodes a manifest written by Bytes.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.SchemaVersion != Version {
		return nil, fmt.Errorf("manifest: unsupported schema version %d", m.SchemaVersion)
	}
	return &m, nil
}

// Digest hashes arbitrary bytes as lowercase hex SHA-256.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestStrings hashes ordered strings with a domain tag, producing a stable
// parameter digest for stages that have no byte input of their own.
func DigestStrings(tag string, values ...string) string {
	h := sha256.New()
	h.Write([]byte(tag))
	for _, v := range values {
		h.Write([]byte{0})
		h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Compare reports the first difference between two manifests, or "" when they
// are identical.
func Compare(a, b *Manifest) string {
	ab, err := a.Bytes()
	if err != nil {
		return err.Error()
	}
	bb, err := b.Bytes()
	if err != nil {
		return err.Error()
	}
	if string(ab) == string(bb) {
		return ""
	}
	return "manifest bytes differ"
}
