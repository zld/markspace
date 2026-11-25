// Package manifest builds, validates and persists the fragment manifest.
//
// The manifest is the only externally consumed output. It lists one entry per
// aligned segment, the per-representation gaps and tfdt regressions recorded
// upstream, and a deterministic SHA-256 over its canonical JSON. Validation
// fails when alignment reported a misaligned boundary, when a segment lacks
// every representation, or when required trace fields are empty. A previously
// persisted, valid manifest is never overwritten by a failed retry.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/align"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/scan"
)

// Segment is one entry of the manifest timeline.
type Segment struct {
	Index      int                       `json:"index"`
	StartNanos int64                     `json:"start_nanos"`
	EndNanos   int64                     `json:"end_nanos"`
	Aligned    bool                      `json:"aligned"`
	Members    map[string]ManifestMember `json:"members"`
}

// ManifestMember is one representation's fragment at a segment.
type ManifestMember struct {
	MFHD      uint32 `json:"mfhd"`
	TFDT      uint64 `json:"tfdt"`
	SHA256    string `json:"sha256"`
	InputPath string `json:"input_path"`
}

// Gap is a JSON-serializable mfhd gap.
type Gap struct {
	Representation string `json:"representation"`
	MissingMFHD    uint32 `json:"missing_mfhd"`
	AfterMFHD      uint32 `json:"after_mfhd"`
}

// Regression is a JSON-serializable tfdt regression.
type Regression struct {
	Representation string `json:"representation"`
	InputPath      string `json:"input_path"`
	MFHD           uint32 `json:"mfhd"`
	PreviousTFDT   uint64 `json:"previous_tfdt"`
	ObservedTFDT   uint64 `json:"observed_tfdt"`
	InputSHA256    string `json:"input_sha256"`
}

// Manifest is the full generated manifest.
type Manifest struct {
	SchemaVersion       string       `json:"schema_version"`
	ManifestVersion     string       `json:"manifest_version"`
	RepresentationCount int          `json:"representation_count"`
	ToleranceNanos      int64        `json:"tolerance_nanos"`
	SegmentCount        int          `json:"segment_count"`
	GapCount            int          `json:"gap_count"`
	RegressionCount     int          `json:"regression_count"`
	Segments            []Segment    `json:"segments"`
	Gaps                []Gap        `json:"gaps"`
	Regressions         []Regression `json:"regressions"`
}

// Build converts an alignment report and scan reports into a manifest.
func Build(version string, alignment *align.Report, reports []*scan.TrackReport, tolerance int64) *Manifest {
	m := &Manifest{
		SchemaVersion:       "1.0",
		ManifestVersion:     version,
		RepresentationCount: alignment.RepresentationCount,
		ToleranceNanos:      tolerance,
		Segments:            []Segment{},
		Gaps:                []Gap{},
		Regressions:         []Regression{},
	}
	for _, b := range alignment.Boundaries {
		seg := Segment{
			Index:      b.Index,
			StartNanos: b.StartNanos,
			EndNanos:   b.EndNanos,
			Aligned:    b.Aligned,
			Members:    map[string]ManifestMember{},
		}
		for repID, mem := range b.Members {
			seg.Members[repID] = ManifestMember{
				MFHD:      mem.MFHD,
				TFDT:      mem.TFDT,
				SHA256:    mem.SHA256,
				InputPath: mem.InputPath,
			}
		}
		m.Segments = append(m.Segments, seg)
	}
	for _, r := range reports {
		for _, g := range r.Gaps {
			m.Gaps = append(m.Gaps, Gap{
				Representation: r.Representation,
				MissingMFHD:    g.MissingMFHD,
				AfterMFHD:      g.AfterMFHD,
			})
		}
		for _, reg := range r.Regressions {
			m.Regressions = append(m.Regressions, Regression{
				Representation: r.Representation,
				InputPath:      reg.InputPath,
				MFHD:           reg.MFHD,
				PreviousTFDT:   reg.PreviousTFDT,
				ObservedTFDT:   reg.ObservedTFDT,
				InputSHA256:    reg.InputSHA256,
			})
		}
	}
	m.SegmentCount = len(m.Segments)
	m.GapCount = len(m.Gaps)
	m.RegressionCount = len(m.Regressions)
	return m
}

// Validate enforces the manifest invariants and returns a classified error
// when the manifest must not be committed.
func (m *Manifest) Validate() error {
	if m.RepresentationCount < 2 {
		return fault.New(fault.CodeManifest, "manifest needs at least two representations")
	}
	if m.SegmentCount == 0 {
		return fault.New(fault.CodeManifest, "manifest contains no segments")
	}
	for _, seg := range m.Segments {
		if !seg.Aligned {
			return fault.New(fault.CodeMisaligned,
				"segment %d is misaligned across representations", seg.Index).
				WithDetail("segment_index", seg.Index)
		}
		if len(seg.Members) != m.RepresentationCount {
			return fault.New(fault.CodeMisaligned,
				"segment %d has %d members, want %d", seg.Index, len(seg.Members), m.RepresentationCount)
		}
		for repID, mem := range seg.Members {
			if mem.SHA256 == "" || mem.InputPath == "" {
				return fault.New(fault.CodeManifest,
					"segment %d member %s is missing provenance", seg.Index, repID)
			}
		}
	}
	return nil
}

// canonicalJSON serializes with fixed field order (struct order) and sorted
// map keys via encoding/json's deterministic map handling.
func (m *Manifest) canonicalJSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// SHA256 returns the hex digest of the canonical manifest JSON.
func (m *Manifest) SHA256() (string, error) {
	data, err := m.canonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// WriteAtomic persists the manifest only after validation. If a valid
// manifest already exists at path it is left untouched and the existing
// digest is returned with replaced=false, so a failed retry can never
// overwrite a prior successful artifact.
func (m *Manifest) WriteAtomic(path string) (digest string, replaced bool, err error) {
	if verr := m.Validate(); verr != nil {
		return "", false, verr
	}
	if existing, lerr := Load(path); lerr == nil {
		existingDigest, _ := existing.SHA256()
		// A valid prior artifact is protected; the caller diffs and reports.
		return existingDigest, false, nil
	}
	data, err := m.canonicalJSON()
	if err != nil {
		return "", false, err
	}
	if err := atomicWrite(path, append(data, '\n')); err != nil {
		return "", false, err
	}
	digest, err = m.SHA256()
	return digest, true, err
}

// Load reads a manifest from path.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: decode failed: %w", err)
	}
	return &m, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
