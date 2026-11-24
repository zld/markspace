// Package provenance records an independently verifiable processing trail.
//
// For every pipeline run it captures, per node: the digest of the node's
// input, the digest of its configuration, the code version, the checkpoint
// state observed at entry and the digest of the produced output. The full
// record is serialized deterministically (sorted keys) so its hash is stable
// across retries and can be recomputed by an external reviewer.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// NodeVersion is the semantic version stamped on each processing node.
const NodeVersion = "1.0.0"

// Entry is the provenance record for one node execution.
type Entry struct {
	Node            string `json:"node"`
	NodeVersion     string `json:"node_version"`
	InputDigest     string `json:"input_digest"`
	ConfigDigest    string `json:"config_digest"`
	CheckpointState string `json:"checkpoint_state"`
	OutputDigest    string `json:"output_digest,omitempty"`
}

// Record is the complete trail for one run.
type Record struct {
	RunID   string  `json:"run_id"`
	Seed    int64   `json:"seed"`
	Entries []Entry `json:"entries"`
}

// Recorder accumulates entries and produces canonical records.
type Recorder struct {
	rec Record
}

// NewRecorder starts a trail for the given run and seed.
func NewRecorder(runID string, seed int64) *Recorder {
	return &Recorder{rec: Record{RunID: runID, Seed: seed}}
}

// Add appends (or replaces) the entry for a node, keeping entries sorted by
// node name for deterministic serialization.
func (r *Recorder) Add(e Entry) {
	if e.NodeVersion == "" {
		e.NodeVersion = NodeVersion
	}
	for i, existing := range r.rec.Entries {
		if existing.Node == e.Node {
			r.rec.Entries[i] = e
			r.sort()
			return
		}
	}
	r.rec.Entries = append(r.rec.Entries, e)
	r.sort()
}

func (r *Recorder) sort() {
	sort.Slice(r.rec.Entries, func(i, j int) bool {
		return r.rec.Entries[i].Node < r.rec.Entries[j].Node
	})
}

// Entries returns a copy of the recorded entries.
func (r *Recorder) Entries() []Entry {
	out := make([]Entry, len(r.rec.Entries))
	copy(out, r.rec.Entries)
	return out
}

// CanonicalJSON serializes the record with sorted object keys. json.Marshal
// already emits struct fields in declaration order; Entry/Record field order
// is fixed and entries are pre-sorted, making the output canonical.
func (r *Recorder) CanonicalJSON() ([]byte, error) {
	return json.MarshalIndent(&r.rec, "", "  ")
}

// Digest returns the SHA-256 of the canonical JSON, the independent review
// anchor for the whole run.
func (r *Recorder) Digest() (string, error) {
	raw, err := r.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// DigestBytes is a convenience for hashing arbitrary payload bytes.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Verify recomputes the digest of a canonical JSON document and compares it
// against the expected hex string, enabling independent re-verification.
func Verify(canonical []byte, expected string) bool {
	return DigestBytes(canonical) == expected
}
