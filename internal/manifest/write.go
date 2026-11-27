// Package manifest writes the time-aligned delivery manifest and verifies it
// after a resume. Writing is idempotent: identical inputs produce byte-equal
// manifests, and re-delivery appends nothing.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediad/internal/model"
	"mediad/internal/plan"
	"mediad/internal/quality"
	"mediad/internal/timeline"
)

// FileName is the fixed manifest name inside the run directory.
const FileName = "manifest.json"

// ManifestVersion pins the on-disk schema.
const ManifestVersion = "mediad-manifest/v1"

// TrackRecord is the per-track section of the manifest.
type TrackRecord struct {
	Track      string            `json:"track"`
	ClockRate  uint32            `json:"clock_rate"`
	StartNS    int64             `json:"start_ns"`
	EndNS      int64             `json:"end_ns"`
	DurationNS int64             `json:"duration_ns"`
	Gaps       []timeline.Gap    `json:"gaps"`
	Duplicates uint64            `json:"duplicates"`
	Action     plan.Action       `json:"action"`
	FrameCount int               `json:"frame_count"`
	OutputSHA  map[string]string `json:"output_sha"`
}

// Manifest is the full deliverable checklist.
type Manifest struct {
	Schema       string            `json:"schema"`
	Algorithms   map[string]string `json:"algorithms"`
	Tracks       []TrackRecord     `json:"tracks"`
	Quality      quality.Report    `json:"quality"`
	StageDigests map[string]string `json:"stage_digests"`
	ManifestSHA  string            `json:"manifest_sha256,omitempty"`
}

// Builder accumulates track records deterministically.
type Builder struct {
	m *Manifest
}

// NewBuilder starts a manifest with pinned algorithm identifiers.
func NewBuilder() *Builder {
	return &Builder{m: &Manifest{
		Schema: ManifestVersion,
		Algorithms: map[string]string{
			"plan":    plan.AlgorithmVersion,
			"quality": quality.MetricAlgorithm,
		},
		Tracks:       []TrackRecord{},
		StageDigests: map[string]string{},
	}}
}

// AddTrack appends one aligned track record.
func (b *Builder) AddTrack(tl *timeline.Timeline, action plan.Action, outputs map[string]string) {
	gaps := tl.Gaps
	if gaps == nil {
		gaps = []timeline.Gap{}
	}
	sha := outputs
	if sha == nil {
		sha = map[string]string{}
	}
	b.m.Tracks = append(b.m.Tracks, TrackRecord{
		Track:      tl.Track,
		ClockRate:  tl.ClockRate,
		StartNS:    int64(tl.StartNS),
		EndNS:      int64(tl.EndNS),
		DurationNS: int64(tl.TotalDurNS),
		Gaps:       gaps,
		Duplicates: tl.Duplicates,
		Action:     action,
		FrameCount: len(tl.Frames),
		OutputSHA:  sha,
	})
}

// SetQuality attaches the measured report and records stage output digests.
func (b *Builder) SetQuality(rep quality.Report, stageDigests map[string]string) {
	b.m.Quality = rep
	for k, v := range stageDigests {
		b.m.StageDigests[k] = v
	}
}

// canonicalJSON sorts map keys and emits stable indentation.
func canonicalJSON(v interface{}) ([]byte, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// digest returns the SHA-256 of the manifest excluding its own digest field.
func (m *Manifest) digest() (string, error) {
	cp := *m
	cp.ManifestSHA = ""
	raw, err := canonicalJSON(&cp)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Write atomically writes the manifest with a self digest. Repeated writes
// with identical content yield byte-equal files.
func (b *Builder) Write(dir string) (*Manifest, error) {
	sort.Slice(b.m.Tracks, func(i, j int) bool { return b.m.Tracks[i].Track < b.m.Tracks[j].Track })
	digest, err := b.m.digest()
	if err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "manifest", 0, err.Error())
	}
	b.m.ManifestSHA = digest
	raw, err := canonicalJSON(b.m)
	if err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "manifest", 0, err.Error())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "manifest", 0, err.Error())
	}
	path := filepath.Join(dir, FileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "manifest", 0, err.Error())
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "manifest", 0, err.Error())
	}
	return b.m, nil
}

// Load reads a manifest back from dir.
func Load(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, model.ErrManifestVerify.At("", 0, "manifest", 0, err.Error())
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "manifest", 0, err.Error())
	}
	return &m, nil
}

// Verify re-computes the self digest and re-hashes every output referenced by
// every track. Any drift returns a located ERR_MANIFEST_VERIFY.
func Verify(dir string) (*Manifest, error) {
	m, err := Load(dir)
	if err != nil {
		return nil, err
	}
	want, err := m.digest()
	if err != nil {
		return nil, err
	}
	if want != m.ManifestSHA {
		return nil, model.ErrManifestVerify.At("", 0, "manifest", 0,
			"manifest self digest mismatch")
	}
	for _, tr := range m.Tracks {
		for rel, wantSHA := range tr.OutputSHA {
			raw, err := os.ReadFile(filepath.Join(dir, rel))
			if err != nil {
				return nil, model.ErrManifestVerify.At(tr.Track, 0, "manifest", 0,
					"missing output "+rel)
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != wantSHA {
				return nil, model.ErrManifestVerify.At(tr.Track, 0, "manifest", 0,
					"output hash drift for "+rel)
			}
		}
	}
	return m, nil
}

// Equal reports whether two manifests carry identical track and quality
// content, used by resume tests.
func Equal(a, b *Manifest) bool {
	ra, _ := canonicalJSON(a)
	rb, _ := canonicalJSON(b)
	return bytes.Equal(ra, rb)
}
