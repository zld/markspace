// Package migrate runs staged manifest migrations with recoverable
// rollback points. A migration always moves through prepare, validate
// and commit; a failure at any stage restores the original manifest and
// leaves cache/index identities consistent with the restored version.
package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"mediacheck/internal/manifest"
)

// Stage names recorded in the migration journal.
const (
	StagePrepare  = "prepare"
	StageValidate = "validate"
	StageCommit   = "commit"
)

// ErrValidationFailed means the validator rejected the candidate and the
// original manifest was restored untouched.
var ErrValidationFailed = errors.New("migration validation failed")

// StageRecord is one journal entry: the stage, the manifest version at
// that point and the stable summary digest.
type StageRecord struct {
	Stage   string `json:"stage"`
	Version int    `json:"version"`
	Summary string `json:"summary"`
}

// IdentityCheck pairs a layer with the hash it must hold after rollback.
type IdentityCheck struct {
	Layer string `json:"layer"` // "content" | "index"
	Hash  string `json:"hash"`
}

// Result is the outcome of a migration run, including the journal and
// the identity assertions a caller can verify against cache and index.
type Result struct {
	Committed bool
	Stages    []StageRecord
	Rollback  bool
	Final     *manifest.Manifest
	Checks    []IdentityCheck
}

// Validator decides whether a candidate manifest is safe to commit.
type Validator func(candidate *manifest.Manifest) error

// Migrator performs staged migrations against an in-memory manifest.
type Migrator struct {
	adapter   manifest.Adapter
	validator Validator
	journal   []StageRecord
}

// NewMigrator wires an adapter and a validation function.
func NewMigrator(v Validator) *Migrator {
	return &Migrator{
		adapter:   manifest.Adapter{},
		validator: v,
	}
}

// Journal returns the stage records in execution order.
func (m *Migrator) Journal() []StageRecord {
	out := make([]StageRecord, len(m.journal))
	copy(out, m.journal)
	return out
}

// Run executes prepare -> validate -> commit on a deep copy of the input
// manifest. When failAtCommit is true the commit stage simulates an
// interrupted write, which must restore the original manifest exactly.
func (m *Migrator) Run(original *manifest.Manifest, failAtCommit bool) (Result, error) {
	m.journal = nil
	snapshot, err := m.snapshot(original)
	if err != nil {
		return Result{}, err
	}

	// Stage 1: prepare — upgrade and stage a candidate.
	candidate, err := m.prepare(original)
	if err != nil {
		return m.rollback(original, snapshot, StagePrepare, err)
	}
	m.record(StagePrepare, candidate.Version, candidate)

	// Stage 2: validate — the candidate must pass every rule.
	if err := m.validator(candidate); err != nil {
		return m.rollback(original, snapshot, StageValidate,
			fmt.Errorf("%w: %v", ErrValidationFailed, err))
	}
	m.record(StageValidate, candidate.Version, candidate)

	// Stage 3: commit — atomically replace, with an interrupt injection
	// point used by the acceptance harness.
	if failAtCommit {
		return m.rollback(original, snapshot, StageCommit,
			errors.New("simulated commit interruption"))
	}
	m.record(StageCommit, candidate.Version, candidate)

	return Result{
		Committed: true,
		Stages:    m.Journal(),
		Final:     candidate,
		Checks:    m.identityChecks(candidate),
	}, nil
}

// prepare produces an upgraded, validated candidate without touching the
// original. It round-trips through the adapter so on-disk semantics are
// identical to a real upgrade.
func (m *Migrator) prepare(original *manifest.Manifest) (*manifest.Manifest, error) {
	buf := &bytes.Buffer{}
	if err := m.adapter.Write(buf, original); err != nil {
		return nil, err
	}
	candidate, err := m.adapter.Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	candidate.Version = manifest.SchemaVersion
	candidate.Summary.Version = manifest.SchemaVersion
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	return candidate, nil
}

func (m *Migrator) rollback(original *manifest.Manifest, snapshot []byte, stage string, cause error) (Result, error) {
	restored, err := m.adapter.Read(bytes.NewReader(snapshot))
	if err != nil {
		return Result{}, fmt.Errorf("rollback restore failed: %w", err)
	}
	if StableDigest(restored) != StableDigest(original) {
		return Result{}, errors.New("rollback digest mismatch")
	}
	return Result{
		Committed: false,
		Rollback:  true,
		Stages:    m.Journal(),
		Final:     restored,
		Checks:    m.identityChecks(restored),
	}, cause
}

func (m *Migrator) record(stage string, version int, mn *manifest.Manifest) {
	m.journal = append(m.journal, StageRecord{
		Stage:   stage,
		Version: version,
		Summary: StableDigest(mn),
	})
}

func (m *Migrator) snapshot(mn *manifest.Manifest) ([]byte, error) {
	buf := &bytes.Buffer{}
	if err := m.adapter.Write(buf, mn); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (m *Migrator) identityChecks(mn *manifest.Manifest) []IdentityCheck {
	return []IdentityCheck{
		{Layer: "content", Hash: mn.Identity.ContentHash},
		{Layer: "index", Hash: indexFingerprint(mn)},
	}
}

// StableDigest hashes every field that defines a run's outcome. It never
// includes CreatedAt, file paths or wall-clock time, so identical inputs
// always produce the identical digest, across machines and repeated runs.
func StableDigest(mn *manifest.Manifest) string {
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)) }
	write(fmt.Sprintf("v=%d|track=%d|", mn.Version, mn.TrackID))
	write("id=" + mn.Identity.ContentHash + "|" +
		mn.Identity.ContainerHash + "|" + mn.Identity.MetadataHash + "|")
	s := mn.Summary
	write(fmt.Sprintf("sum=%s|n=%d|k=%d|f=%d|max=%d|rate=%d|trust=%t|",
		s.Codec, s.SampleCount, s.KeyFrameCount, s.FaultCount,
		s.MaxKeyInterval, s.ClockRate, s.TimeTrusted))
	// Sort fault-class keys: Go map iteration order is randomized and a
	// stable digest must not depend on it.
	classes := make([]string, 0, len(s.FaultsByClass))
	for class := range s.FaultsByClass {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		write("c:" + class + "=" + fmt.Sprint(s.FaultsByClass[class]) + "|")
	}
	for _, r := range mn.Samples {
		write(fmt.Sprintf("s:%d,%s,k=%t,%d-%d,@%d+%d,t%d-%d|",
			r.Index, r.Codec, r.KeyFrame, r.FirstSeq, r.LastSeq,
			r.ByteOffset, r.ByteLength, r.TimeStart, r.TimeEnd))
	}
	for _, f := range mn.Faults {
		write(fmt.Sprintf("e:%s,%d-%d,@%d+%d,t%d-%d|",
			f.Class, f.SeqFirst, f.SeqLast, f.ByteOffset, f.ByteLength,
			f.TimeStart, f.TimeEnd))
	}
	return "sha256:manifest:" + hex.EncodeToString(h.Sum(nil))
}

// indexFingerprint derives the index identity a restored manifest must
// match, so callers can assert cache/index consistency after rollback.
func indexFingerprint(mn *manifest.Manifest) string {
	h := sha256.New()
	h.Write([]byte(mn.Identity.ContainerHash))
	h.Write([]byte{0})
	h.Write([]byte(mn.Identity.MetadataHash))
	for _, r := range mn.Samples {
		h.Write([]byte(fmt.Sprintf("%d:%d:%d", r.Index, r.ByteOffset, r.ByteLength)))
	}
	return "sha256:index:" + hex.EncodeToString(h.Sum(nil))
}
