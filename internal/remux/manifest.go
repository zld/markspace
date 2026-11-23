package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FragmentManifest describes one output chunk inside a slot stream.
type FragmentManifest struct {
	OutputSeq    int    `json:"output_seq"`
	OriginSeq    int    `json:"origin_seq"`
	InputOrder   int    `json:"input_order"`
	File         string `json:"file"`
	CorrectedDTS uint64 `json:"corrected_dts"`
	// Duration is the total sample duration in the track's timescale.
	Duration uint64 `json:"duration"`
	// PayloadBytes is the number of compressed (mdat payload) bytes.
	PayloadBytes int `json:"payload_bytes"`
	Timescale    uint32 `json:"timescale"`
	SampleCount    int    `json:"sample_count"`
	Bytes          int    `json:"bytes"`
	Repeated       bool   `json:"repeated"`
	Late           bool   `json:"late"`
	PayloadHashHex string `json:"payload_hash"`
}

// SlotManifest describes one remuxed track stream.
type SlotManifest struct {
	Slot      string             `json:"slot"`
	TrackID   uint32             `json:"track_id"`
	MediaType string             `json:"media_type"`
	CodecKey  string             `json:"codec_key"`
	Timescale uint32             `json:"timescale"`
	InitFile  string             `json:"init_file"`
	Fragments []FragmentManifest `json:"fragments"`
}

// Manifest is the deterministic, JSON-serialized output description.
type Manifest struct {
	Slots       []SlotManifest `json:"slots"`
	SummaryHash string         `json:"summary_hash"`
}

// NewManifest returns an empty manifest.
func NewManifest() *Manifest { return &Manifest{} }

func (m *Manifest) sortSlots() {
	sort.SliceStable(m.Slots, func(i, j int) bool {
		return m.Slots[i].Slot < m.Slots[j].Slot
	})
	for i := range m.Slots {
		sort.SliceStable(m.Slots[i].Fragments, func(a, b int) bool {
			return m.Slots[i].Fragments[a].OutputSeq <
				m.Slots[i].Fragments[b].OutputSeq
		})
	}
}

// canonicalJSON serializes the manifest without the summary field so the
// hash covers exactly the content it summarizes.
func (m *Manifest) canonicalJSON() ([]byte, error) {
	cp := *m
	cp.SummaryHash = ""
	return json.MarshalIndent(&cp, "", "  ")
}

// CanonicalJSON exposes the stable serialization used for the summary
// hash, allowing external verifiers to recompute it byte-identically.
func (m *Manifest) CanonicalJSON() ([]byte, error) { return m.canonicalJSON() }

// computeSummaryHash hashes the canonical content; identical inputs always
// produce an identical hash, including across repeated pipeline runs.
func (m *Manifest) computeSummaryHash() string {
	raw, err := m.canonicalJSON()
	if err != nil {
		// All manifest types here are JSON-safe; surface rather than hide.
		return fmt.Sprintf("hash-error:%v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// writeManifest stores manifest.json under root via a safe path.
func (w *Writer) writeManifest(m *Manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path, err := SafePath(w.root, "manifest.json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// LoadManifest reads a manifest previously written under root.
func LoadManifest(root string) (*Manifest, error) {
	path, err := SafePath(root, "manifest.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
