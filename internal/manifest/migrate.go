package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"

	"replaypipe/internal/store"
	"replaypipe/internal/timebase"
)

// MigrationResult reports the outcome of a version migration.
type MigrationResult struct {
	FromVersion int      `json:"from_version"`
	ToVersion   int      `json:"to_version"`
	Migrated    bool     `json:"migrated"`
	Idempotent  bool     `json:"idempotent"`
	Rollback    string   `json:"rollback_path"`
	ManifestSHA string   `json:"manifest_sha256"`
	PreviousSHA string   `json:"previous_sha256"`
	Steps       []string `json:"steps"`
}

// MigrateOptions controls the migration run.
type MigrateOptions struct {
	// InterruptAfterStage stops after staging and journaling, before the
	// atomic switch, so a rerun can prove rollback retention and idempotency.
	InterruptAfterStage bool
}

// Migrate reads path (any supported version), normalises to the canonical
// version, validates the self-digest and atomically switches the file. The
// original file is preserved as <name>.rollback. Steps are reported in the
// order they executed: parse, normalise, validate, switch.
func Migrate(path string, opts MigrateOptions) (*MigrationResult, *timebase.Error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, timebase.Fail(timebase.CodeManifestInvalid, 0, -1).Wrap(err, "read manifest")
	}
	var hdr struct {
		Version int `json:"version"`
	}
	if err := jsonUnmarshal(data, &hdr); err != nil {
		return nil, timebase.Fail(timebase.CodeManifestInvalid, 0, -1).Wrap(err, "decode manifest version")
	}
	res := &MigrationResult{FromVersion: hdr.Version, ToVersion: Version}
	res.Steps = append(res.Steps, "parse")
	adapter := Adapter{}
	m, e := adapter.Read(data)
	if e != nil {
		return nil, e
	}
	res.Steps = append(res.Steps, "normalise")
	// Re-encoding recomputes the self-digest and canonical field order.
	encoded, e := Encode(m)
	if e != nil {
		return nil, e
	}
	if want := m.ComputeDigest(); m.Digest != "" && m.Digest != want {
		return nil, timebase.Newf(timebase.CodeDigestMismatch, 0, -1,
			"manifest digest %s does not match %s", m.Digest, want)
	}
	res.Steps = append(res.Steps, "validate-digest")
	if hdr.Version == Version && string(data) == string(encoded) {
		res.Idempotent = true
		res.ManifestSHA = m.Digest
		res.PreviousSHA = m.Digest
		res.Rollback = path + ".rollback"
		res.Steps = append(res.Steps, "already-current")
		return res, nil
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	commit, e := store.Commit(dir, base, encoded, nil, store.CommitOptions{FailBeforeSwitch: opts.InterruptAfterStage})
	if e != nil {
		return nil, e
	}
	res.Steps = append(res.Steps, "atomic-switch")
	res.Migrated = true
	res.ManifestSHA = commit.ManifestSHA
	res.PreviousSHA = commit.PreviousSHA
	res.Rollback = commit.BackupPath
	return res, nil
}

// jsonUnmarshal is a thin indirection so migrate.go does not import encoding
// json directly twice in larger refactors.
func jsonUnmarshal(data []byte, v any) error { return jsonUnmarshalImpl(data, v) }

func jsonUnmarshalImpl(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
