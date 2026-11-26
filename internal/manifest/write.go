// Package manifest renders the exception manifest: anomalies within a requested
// time window with their type, track, time range, source offset, checksums and
// recovery state, in a stable order so repeated runs hash identically.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediad/internal/model"
)

// Manifest is the serialized exception list for one time window.
type Manifest struct {
	Schema       string          `json:"schema"`
	SourceHash   string          `json:"source_hash"`
	FromNanos    int64           `json:"from_nanos"`
	ToNanos      int64           `json:"to_nanos"`
	AnomalyCount int             `json:"anomaly_count"`
	Anomalies    []model.Anomaly `json:"anomalies"`
}

// SchemaVersion is bumped only on incompatible manifest format changes.
const SchemaVersion = "mediad-manifest-1"

// Window filters anomalies to those intersecting [from,to] nanoseconds and
// returns a fully populated, stably ordered manifest.
func Window(sourceHash string, from, to int64, in []model.Anomaly) *Manifest {
	if from > to {
		from, to = to, from
	}
	out := make([]model.Anomaly, 0, len(in))
	for _, a := range in {
		// An anomaly with a zero-length or unset time range is kept when its
		// start lies inside the window; otherwise require real intersection.
		start := a.StartNanos
		end := a.EndNanos
		if end < start {
			end = start
		}
		if end < from || start > to {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StableKey() < out[j].StableKey()
	})
	return &Manifest{
		Schema: SchemaVersion, SourceHash: sourceHash,
		FromNanos: from, ToNanos: to,
		AnomalyCount: len(out), Anomalies: out,
	}
}

// Marshal renders the manifest to deterministic JSON. json.Marshal on a sorted
// slice with fixed struct tags is itself deterministic, and a trailing newline
// makes the file friendly to terminal tooling without affecting the hash
// (which is computed over the canonical bytes below).
func (m *Manifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("manifest: marshal: %w", err)
	}
	return append(data, '\n'), nil
}

// Hash is the content digest of the canonical manifest bytes (without the
// trailing newline), proving repeated runs produce identical manifests.
func (m *Manifest) Hash() (string, error) {
	canonical, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("manifest: hash: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Write atomically persists the manifest under root/manifests named by its
// content hash, and also writes the human-facing latest.json pointer.
func Write(root string, m *Manifest) (path, hash string, err error) {
	hash, err = m.Hash()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(root, "manifests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	data, err := m.Marshal()
	if err != nil {
		return "", "", err
	}
	final := filepath.Join(dir, hash+".json")
	tmp, err := os.CreateTemp(dir, "manifest-*.tmp")
	if err != nil {
		return "", "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", "", err
	}
	if err := os.Rename(name, final); err != nil {
		return "", "", err
	}
	// Deterministic latest pointer (not part of the hashed content).
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), data, 0o644); err != nil {
		return "", "", err
	}
	return final, hash, nil
}
