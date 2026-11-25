// Package pipeline orchestrates the offline ingest -> normalize ->
// fingerprint -> archive stages. Each stage derives a stable key from its
// input digest and parameters, so repeated delivery reuses prior results
// instead of re-creating manifest rows or changing artifact hashes.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"

	"mediavault/internal/timeline"
)

// Stage is one named processing phase with deterministic inputs.
type Stage struct {
	Name        string
	InputDigest string
	Params      string
}

// Key returns the stable content-addressed key for a stage invocation.
func (s Stage) Key() string {
	h := sha256.New()
	h.Write([]byte(s.Name))
	h.Write([]byte{0})
	h.Write([]byte(s.InputDigest))
	h.Write([]byte{0})
	h.Write([]byte(s.Params))
	return hex.EncodeToString(h.Sum(nil))
}

// State tracks whether a stage has already completed in this or a prior
// process, enabling idempotent re-runs.
type State struct {
	completed map[string]StageResult
	order     []string
}

// StageResult records a finished stage's key and output digest.
type StageResult struct {
	Stage        string
	Key          string
	OutputDigest string
}

// NewState creates an empty stage state.
func NewState() *State {
	return &State{completed: map[string]StageResult{}}
}

// MarkCompleted records a finished stage keyed by its stable key.
func (st *State) MarkCompleted(res StageResult) {
	if _, ok := st.completed[res.Key]; ok {
		return
	}
	st.completed[res.Key] = res
	st.order = append(st.order, res.Key)
}

// IsDone reports whether a stage with this key already ran.
func (st *State) IsDone(key string) bool {
	_, ok := st.completed[key]
	return ok
}

// Results returns completed stage results in insertion order.
func (st *State) Results() []StageResult {
	out := make([]StageResult, 0, len(st.order))
	for _, k := range st.order {
		out = append(out, st.completed[k])
	}
	return out
}

// DigestBytes is a small helper that hashes arbitrary stage input bytes.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// StageOrder is the canonical execution order shared by every subsystem.
var StageOrder = []string{
	timeline.StageIngest,
	timeline.StageNormalize,
	timeline.StageFingerprint,
	timeline.StageArchive,
}
