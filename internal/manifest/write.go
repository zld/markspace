// Package manifest renders the deliverable segment list deterministically,
// so a resumed run produces byte-identical output.
package manifest

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
)

// SegmentEntry is one segment of one representation.
type SegmentEntry struct {
	Index   int    `json:"index"`
	Rep     string `json:"rep"`
	StartTC string `json:"start_tc"`
	Frames  int64  `json:"frames"`
	SHA256  string `json:"sha256"`
	Source  string `json:"keyframe_source"`
}

// Manifest is the full traceable deliverable.
type Manifest struct {
	Seed     int64          `json:"seed"`
	Timebase string         `json:"timebase"`
	Params   string         `json:"params"`
	Segments []SegmentEntry `json:"segments"`
}

// canonical renders the manifest with stable field and segment order.
func canonical(m *Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// Write emits the deterministic JSON form.
func Write(w io.Writer, m *Manifest) error {
	raw, err := canonical(m)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

// Digest is the manifest's content hash.
func Digest(m *Manifest) ([32]byte, error) {
	raw, err := canonical(m)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

// Equal compares two manifests by digest with a fixed-length
// constant-time compare.
func Equal(a, b *Manifest) (bool, error) {
	da, err := Digest(a)
	if err != nil {
		return false, err
	}
	db, err := Digest(b)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(da[:], db[:]) == 1, nil
}
