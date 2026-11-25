// Package provenance records a verifiable processing manifest: exact input
// and index versions, per-stage status, index hit/miss evidence, damaged
// offsets, skip reasons and a hash per produced segment. The manifest is
// only published through an atomic rename, so no run can leave a partial
// success record behind.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"mediad/internal/errors"
)

// StageStatus is the terminal state of one pipeline stage.
type StageStatus string

const (
	StagePending   StageStatus = "pending"
	StageRunning   StageStatus = "running"
	StageSucceeded StageStatus = "succeeded"
	StagePartial   StageStatus = "partial" // finished with structural defects
	StageFailed    StageStatus = "failed"
)

// Stage records one pipeline phase.
type Stage struct {
	Name      string      `json:"name"`
	Status    StageStatus `json:"status"`
	StartedAt string      `json:"started_at"`
	EndedAt   string      `json:"ended_at,omitempty"`
	Detail    string      `json:"detail,omitempty"`
}

// SegmentRecord is the per-segment provenance entry.
type SegmentRecord struct {
	ID          uint64   `json:"id"`
	SHA256      string   `json:"sha256"`
	Corrupt     bool     `json:"corrupt"`
	Reasons     []string `json:"reasons,omitempty"`
	Duplicate   bool     `json:"duplicate"`
	FirstOfHash uint64   `json:"first_of_hash"`
}

// Manifest is the complete, hashable processing record.
type Manifest struct {
	SchemaVersion int     `json:"schema_version"`
	FixtureID     string  `json:"fixture_id"`
	Codec         string  `json:"codec"`
	InputHash     string  `json:"input_hash"`
	IndexHash     string  `json:"index_hash"`
	CreatedAt     string  `json:"created_at"`
	Stages        []Stage `json:"stages"`
	// Index evidence.
	IndexHits      int      `json:"index_hits"`
	IndexMisses    int      `json:"index_misses"`
	HitRate        float64  `json:"hit_rate"`
	DamagedOffsets []int64  `json:"damaged_offsets"`
	SkipReasons    []string `json:"skip_reasons"`
	// Results.
	Segments       []SegmentRecord `json:"segments"`
	SilenceVerdict string          `json:"silence_verdict"`
}

// New initializes a manifest bound to the given input versions.
func New(fixtureID, codec, inputHash, indexHash string) *Manifest {
	return &Manifest{
		SchemaVersion:  1,
		FixtureID:      fixtureID,
		Codec:          codec,
		InputHash:      inputHash,
		IndexHash:      indexHash,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		SilenceVerdict: "undecidable",
	}
}

// SetStage upserts a stage by name.
func (m *Manifest) SetStage(s Stage) {
	for i := range m.Stages {
		if m.Stages[i].Name == s.Name {
			m.Stages[i] = s
			return
		}
	}
	m.Stages = append(m.Stages, s)
}

// ContentHash is the SHA-256 over the canonical JSON encoding of the whole
// document including timestamps.
func (m *Manifest) ContentHash() (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", errs.New(errs.Fatal, errs.CodeInternal, "encode manifest: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// CanonicalHash is the run-independent digest: wall-clock fields are
// blanked first, so retrying the same input version yields the same hash
// even though CreatedAt and stage timings differ.
func (m *Manifest) CanonicalHash() (string, error) {
	cp := *m
	cp.CreatedAt = ""
	cp.Stages = append([]Stage(nil), m.Stages...)
	for i := range cp.Stages {
		cp.Stages[i].StartedAt = ""
		cp.Stages[i].EndedAt = ""
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return "", errs.New(errs.Fatal, errs.CodeInternal, "encode manifest: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Publish writes the manifest atomically (temp file + fsync + rename) and
// returns its content hash. No half-written manifest can be observed.
func (m *Manifest) Publish(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", errs.New(errs.Fatal, errs.CodeIOFailure, "manifest dir: %v", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", errs.New(errs.Fatal, errs.CodeInternal, "encode manifest: %v", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", errs.New(errs.Fatal, errs.CodeIOFailure, "create manifest: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", errs.New(errs.Fatal, errs.CodeIOFailure, "write manifest: %v", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", errs.New(errs.Fatal, errs.CodeIOFailure, "sync manifest: %v", err)
	}
	if err := f.Close(); err != nil {
		return "", errs.New(errs.Fatal, errs.CodeIOFailure, "close manifest: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", errs.New(errs.Fatal, errs.CodeIOFailure, "rename manifest: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Load reads a previously published manifest.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeIOFailure, "read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeIOFailure, "decode manifest: %v", err)
	}
	return &m, nil
}
