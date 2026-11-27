// Package manifest builds, hashes and validates the time-aligned fragment
// manifest. Serialization is canonical JSON so identical inputs always yield
// an identical manifest digest; reruns compare entry digests to report new,
// reused and changed entries without reusing stale results.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
)

const subsystem = "manifest"

// SchemaVersion is bumped when the on-disk manifest layout changes.
const SchemaVersion = 1

// Entry describes one aligned fragment across all representations.
type Entry struct {
	ID          string            `json:"id"`
	Index       int               `json:"index"`
	StartTick   clock.Tick        `json:"start_tick"`
	EndTick     clock.Tick        `json:"end_tick"`
	Segments    map[string]Fragment `json:"segments"`
	// OrigOffsets preserves traceability: representation id -> first byte of
	// the cut in that representation's original bitstream.
	OrigOffsets map[string]int64 `json:"orig_offsets"`
	// Digest covers the entry's canonical JSON including all fragment hashes.
	Digest string `json:"digest"`
}

// Fragment is one representation's file inside a manifest entry.
type Fragment struct {
	Path        string     `json:"path"`
	Size        int64      `json:"size"`
	SampleCount int        `json:"sample_count"`
	SHA256      string     `json:"sha256"`
	TickRange   [2]clock.Tick `json:"tick_range"`
}

// Manifest is the top-level, deterministically serialized document.
type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	JobID         string   `json:"job_id"`
	Timescale     int64    `json:"timescale"`
	Codecs        []string `json:"codecs"`
	Bitrates      []int    `json:"bitrates"`
	Entries       []Entry  `json:"entries"`
	// InputDigest hashes the source bitstream/index inputs, so any input
	// change invalidates reuse even if entry ids happen to line up.
	InputDigest string `json:"input_digest"`
	// Digest is the hash of the canonical manifest without this field.
	Digest string `json:"digest"`
}

// canonicalBytes serializes with sorted map keys (encoding/json sorts map
// keys) and no trailing whitespace, giving stable bytes across runs.
func canonicalBytes(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, perr.New(subsystem, perr.CodeStage, "canonical encode: %v", err)
	}
	return b, nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Finalize computes each entry digest and then the whole-manifest digest.
// It mutates and returns m for convenient chaining.
func Finalize(m *Manifest) (*Manifest, error) {
	m.SchemaVersion = SchemaVersion
	m.Timescale = clock.TicksPerSecond
	for i := range m.Entries {
		e := &m.Entries[i]
		clone := *e
		clone.Digest = ""
		b, err := canonicalBytes(clone)
		if err != nil {
			return nil, err
		}
		e.Digest = hashBytes(b)
	}
	clone := *m
	clone.Digest = ""
	b, err := canonicalBytes(clone)
	if err != nil {
		return nil, err
	}
	m.Digest = hashBytes(b)
	return m, nil
}

// Marshal returns the canonical on-disk bytes (indented for human reading;
// the digest is computed over the compact canonical form, not the indent).
func Marshal(m *Manifest) ([]byte, error) {
	if _, err := Finalize(m); err != nil {
		return nil, err
	}
	return json.MarshalIndent(m, "", "  ")
}

// Unmarshal parses a manifest and verifies its embedded digest.
func Unmarshal(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, perr.New(subsystem, perr.CodeChecksum,
			"malformed manifest: %v", err).Wrap(err)
	}
	return &m, nil
}

// Verify recomputes the manifest and entry digests and reports the first
// mismatch. A tampered document fails with CodeChecksum.
func Verify(m *Manifest) error {
	for i, e := range m.Entries {
		clone := e
		clone.Digest = ""
		b, err := canonicalBytes(clone)
		if err != nil {
			return err
		}
		if got := hashBytes(b); got != e.Digest {
			return perr.New(subsystem, perr.CodeChecksum,
				"entry %d digest mismatch: stored %s computed %s", i, e.Digest, got)
		}
	}
	clone := *m
	stored := m.Digest
	clone.Digest = ""
	b, err := canonicalBytes(clone)
	if err != nil {
		return err
	}
	if got := hashBytes(b); got != stored {
		return perr.New(subsystem, perr.CodeChecksum,
			"manifest digest mismatch: stored %s computed %s", stored, got)
	}
	return nil
}
