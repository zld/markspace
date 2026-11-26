// Package migrate runs three-phase manifest migrations — read, shadow
// validation, commit — with atomic replace and byte-exact rollback.
package migrate

import (
	"crypto/sha256"
	"os"

	"streamcheck/core"
)

// Migration transforms a manifest from an old format to a new one. The
// source manifest is never modified; the destination is replaced
// atomically and only after shadow validation passes.
type Migration struct {
	Src       string
	Dst       string
	Transform func([]byte) ([]byte, error)
	Validate  func([]byte) error

	oldHash [32]byte
	tmp     string
}

// Read is phase 1: load the source manifest and pin its digest so
// rollback can prove the old format stayed readable and untouched.
func (m *Migration) Read() ([]byte, error) {
	data, err := os.ReadFile(m.Src)
	if err != nil {
		return nil, core.E(core.KindInput, "migrate-read", "cannot read source manifest", err).At(m.Src, -1)
	}
	m.oldHash = sha256.Sum256(data)
	return data, nil
}

// Shadow is phase 2: transform the old bytes, validate the result, and
// stage it to a temporary file next to the destination. Nothing is
// visible at the destination yet.
func (m *Migration) Shadow(old []byte) error {
	if m.Transform == nil || m.Validate == nil {
		return core.E(core.KindMigrate, "migrate-shadow", "transform and validate hooks are required", nil)
	}
	newData, err := m.Transform(old)
	if err != nil {
		return core.E(core.KindMigrate, "migrate-transform", "transform failed", err).At(m.Src, -1)
	}
	if err := m.Validate(newData); err != nil {
		return core.E(core.KindMigrate, "migrate-shadow", "shadow validation failed", err).At(m.Dst, -1)
	}
	tmp, err := stageFile(m.Dst, newData)
	if err != nil {
		return core.E(core.KindMigrate, "migrate-shadow", "cannot stage output", err).At(m.Dst, -1)
	}
	m.tmp = tmp
	return nil
}

// Commit is phase 3: atomically move the staged file into place.
func (m *Migration) Commit() error {
	if m.tmp == "" {
		return core.E(core.KindMigrate, "migrate-commit", "nothing staged", nil).At(m.Dst, -1)
	}
	if err := commitFile(m.tmp, m.Dst); err != nil {
		return core.E(core.KindMigrate, "migrate-commit", "atomic rename failed", err).At(m.Dst, -1)
	}
	m.tmp = ""
	return nil
}

// Rollback abandons the migration: the staged file is removed and the
// source manifest is verified byte-identical to what Read observed.
func (m *Migration) Rollback() error {
	if m.tmp != "" {
		if err := os.Remove(m.tmp); err != nil && !os.IsNotExist(err) {
			return core.E(core.KindMigrate, "migrate-rollback", "cannot remove staged file", err).At(m.tmp, -1)
		}
		m.tmp = ""
	}
	data, err := os.ReadFile(m.Src)
	if err != nil {
		return core.E(core.KindMigrate, "migrate-rollback", "source manifest unreadable", err).At(m.Src, -1)
	}
	if sha256.Sum256(data) != m.oldHash {
		return core.E(core.KindMigrate, "migrate-rollback",
			"source manifest changed during migration", nil).At(m.Src, -1)
	}
	return nil
}

// Run executes all three phases. Any failure triggers rollback; the
// returned error is structured.
func (m *Migration) Run() error {
	old, err := m.Read()
	if err != nil {
		return err
	}
	if err := m.Shadow(old); err != nil {
		if rb := m.Rollback(); rb != nil {
			return core.E(core.KindMigrate, "migrate-run",
				err.Error()+"; rollback also failed: "+rb.Error(), nil)
		}
		return err
	}
	return m.Commit()
}
