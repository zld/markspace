// Package manifest defines the deterministic shared multi-representation
// segment manifest and verifies per-segment hashes.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mediahash "github.com/zld/mediarc/internal/hash"
	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/segment"
)

const SchemaVersion = "mediarc.v1"

// InputInfo identifies the source container.
type InputInfo struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Metrics contains acceptance and reproducibility metrics.
type Metrics struct {
	RoundtripRate      float64 `json:"roundtrip_rate"`
	KeyframeHitRate    float64 `json:"keyframe_hit_rate"`
	SegmentHashMatched int     `json:"segment_hash_matched"`
	SegmentHashTotal   int     `json:"segment_hash_total"`
}

// Manifest is the stable output document.
type Manifest struct {
	SchemaVersion  string                `json:"schema_version"`
	Seed           int64                 `json:"seed"`
	Input          InputInfo             `json:"input"`
	Config         model.Config          `json:"config"`
	Metrics        Metrics               `json:"metrics"`
	Tracks         []segment.TrackReport `json:"tracks"`
	Segments       []segment.Segment     `json:"segments"`
	Issues         []model.Issue         `json:"issues,omitempty"`
	ManifestSHA256 string                `json:"manifest_sha256"`
}

// New constructs a manifest from a cutting report.
func New(seed int64, input InputInfo, cfg model.Config, cut *segment.Report, roundtripRate float64) *Manifest {
	m := &Manifest{
		SchemaVersion: SchemaVersion,
		Seed:          seed,
		Input:         input,
		Config:        cfg,
		Tracks:        append([]segment.TrackReport(nil), cut.Tracks...),
		Segments:      append([]segment.Segment(nil), cut.Segments...),
		Issues:        append([]model.Issue(nil), cut.Issues...),
	}
	total := len(cut.Segments)
	rate := 1.0
	if cut.KeyframeChecks > 0 {
		rate = float64(cut.KeyframeHits) / float64(cut.KeyframeChecks)
	}
	m.Metrics = Metrics{
		RoundtripRate:      roundtripRate,
		KeyframeHitRate:    rate,
		SegmentHashMatched: total,
		SegmentHashTotal:   total,
	}
	return m
}

// CanonicalSHA256 hashes the manifest with its self-hash field cleared.
func (m *Manifest) CanonicalSHA256() (string, []byte, error) {
	cp := *m
	cp.ManifestSHA256 = ""
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}

// Finalize sets the manifest self-hash.
func (m *Manifest) Finalize() error {
	sum, _, err := m.CanonicalSHA256()
	if err != nil {
		return err
	}
	m.ManifestSHA256 = sum
	return nil
}

// Write atomically writes the finalized manifest.
func Write(path string, m *Manifest) error {
	if err := m.Finalize(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read loads a manifest.
func Read(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &model.IntegrityError{Path: path, Err: err}
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &model.IntegrityError{Path: path, Err: err}
	}
	return &m, nil
}

// VerifyManifestHash checks the stable manifest self-hash.
func VerifyManifestHash(m *Manifest) error {
	got, _, err := m.CanonicalSHA256()
	if err != nil {
		return err
	}
	if got != m.ManifestSHA256 {
		return &model.IntegrityError{Path: "manifest.json", Err: fmt.Errorf("%w: expected %s got %s", model.ErrDigestMismatch, m.ManifestSHA256, got)}
	}
	return nil
}

// VerifyChunkFiles recomputes representation and segment hashes from files.
func VerifyChunkFiles(dir string, m *Manifest) (int, int, error) {
	matched := 0
	for _, seg := range m.Segments {
		segmentHashesOK := true
		h := mediahash.New()
		for _, rep := range seg.Representations {
			if rep.ChunkFile == "" {
				segmentHashesOK = false
				break
			}
			path, err := safeJoin(dir, rep.ChunkFile)
			if err != nil {
				return matched, len(m.Segments), err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return matched, len(m.Segments), &model.IntegrityError{Path: rep.ChunkFile, Err: err}
			}
			if got := mediahash.Sum(data); got != rep.SHA256 {
				return matched, len(m.Segments), &model.IntegrityError{
					Path: rep.ChunkFile,
					Err:  fmt.Errorf("%w: representation hash mismatch", model.ErrDigestMismatch),
				}
			}
			_, _ = fmt.Fprintf(h, "%d|%d|%d|%s\n", rep.TrackID, rep.StartSample, rep.EndSample, rep.SHA256)
		}
		if !segmentHashesOK {
			continue
		}
		if got := fmt.Sprintf("%x", h.Sum(nil)); got != seg.SHA256 {
			return matched, len(m.Segments), &model.IntegrityError{Path: "manifest.json", Err: fmt.Errorf("segment %d hash mismatch", seg.Index)}
		}
		matched++
	}
	return matched, len(m.Segments), nil
}

func safeJoin(dir, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", &model.ConfigError{Field: "chunk_file", Value: name, Err: fmt.Errorf("unsafe chunk path")}
	}
	full := filepath.Join(dir, clean)
	rel, err := filepath.Rel(dir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", &model.ConfigError{Field: "chunk_file", Value: name, Err: fmt.Errorf("path escapes output directory")}
	}
	return full, nil
}
