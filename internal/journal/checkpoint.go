// Package journal persists checkpoints and case results atomically so an
// interrupted run can resume without recomputing or overwriting committed
// work. State is namespaced by a digest of the manifest, parser versions
// and processing parameters; a different configuration gets a fresh state.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ConfigDigest is the identity of one processing configuration.
type ConfigDigest struct {
	ManifestSHA   string            `json:"manifest_sha256"`
	ParserVersions map[string]string `json:"parser_versions"`
	Parameters    map[string]string `json:"parameters"`
}

// Digest renders the canonical hash. Map keys are sorted so the digest is
// independent of Go's randomized map iteration.
func (c ConfigDigest) Digest() (string, error) {
	keys := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	write(c.ManifestSHA)
	for _, k := range keys(c.Parameters) {
		write("p:" + k + "=" + c.Parameters[k])
	}
	pvKeys := make([]string, 0, len(c.ParserVersions))
	for k := range c.ParserVersions {
		pvKeys = append(pvKeys, k)
	}
	sort.Strings(pvKeys)
	for _, k := range pvKeys {
		write("v:" + k + "=" + c.ParserVersions[k])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Status of one case item.
type Status string

const (
	StatusCommitted Status = "committed"
	StatusFailed    Status = "failed"
)

// Item is one checkpoint entry keyed by case id.
type Item struct {
	CaseID   string          `json:"case_id"`
	Status   Status          `json:"status"`
	Result   json.RawMessage `json:"result,omitempty"`
	Errors   []SourcedError  `json:"errors,omitempty"`
	Attempts int             `json:"attempts"`
}

// SourcedError is the persisted form of model.SourceError (decoupled so the
// journal never imports processing internals).
type SourcedError struct {
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
	Loc  string `json:"loc"`
}

// Journal is the on-disk state for one configuration digest.
type Journal struct {
	root   string
	digest string
}

// Open resolves the state directory for a digest. It never touches inputs.
func Open(stateDir string, digest string) *Journal {
	return &Journal{root: filepath.Join(stateDir, digest), digest: digest}
}

func (j *Journal) dir() string { return j.root }

// Digest returns the resolved digest name.
func (j *Journal) Digest() string { return j.digest }

// Ensure creates the state tree with restrictive permissions.
func (j *Journal) Ensure() error {
	return os.MkdirAll(filepath.Join(j.root, "items"), 0o755)
}

func (j *Journal) itemPath(caseID string) string {
	return filepath.Join(j.root, "items", caseID+".json")
}

// Status reports the checkpoint state of a case. Missing means "never run".
func (j *Journal) Status(caseID string) (Status, *Item, error) {
	data, err := os.ReadFile(j.itemPath(caseID))
	if os.IsNotExist(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var it Item
	if err := json.Unmarshal(data, &it); err != nil {
		return "", nil, fmt.Errorf("corrupt checkpoint %s: %w", caseID, err)
	}
	return it.Status, &it, nil
}

// IsCommitted is true only when a case previously completed successfully.
// Failed items are intentionally not committed, so retries remain possible.
func (j *Journal) IsCommitted(caseID string) (bool, error) {
	st, _, err := j.Status(caseID)
	return st == StatusCommitted, err
}

// Commit atomically writes a successful result. An existing committed item
// is never overwritten; the caller is expected to skip those first.
func (j *Journal) Commit(it Item) error {
	if st, _, err := j.Status(it.CaseID); err != nil {
		return err
	} else if st == StatusCommitted {
		return fmt.Errorf("refusing to overwrite committed item %s", it.CaseID)
	}
	it.Status = StatusCommitted
	return j.atomicWrite(j.itemPath(it.CaseID), it)
}

// RecordFailure atomically writes a failed attempt without committing any
// index, leaving the case retryable on the next run.
func (j *Journal) RecordFailure(caseID string, errs []SourcedError, attempts int) error {
	return j.atomicWrite(j.itemPath(caseID), Item{
		CaseID: caseID, Status: StatusFailed, Errors: errs, Attempts: attempts,
	})
}

// List returns every checkpoint item, ordered by case id for determinism.
func (j *Journal) List() ([]Item, error) {
	entries, err := os.ReadDir(filepath.Join(j.root, "items"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var it Item
		data, err := os.ReadFile(filepath.Join(j.root, "items", e.Name()))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CaseID < out[k].CaseID })
	return out, nil
}

// atomicWrite writes to a temp file in the same directory and renames it
// over the target, so readers never observe a half-written checkpoint.
func (j *Journal) atomicWrite(path string, v interface{}) error {
	if err := j.Ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(j.root, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// SHA256File returns the hex sha256 of a file's bytes.
func SHA256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
