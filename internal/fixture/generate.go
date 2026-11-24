package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"archive/internal/model"
)

// Artifact describes one generated file.
type Artifact struct {
	Spec          string       `json:"spec"`
	Format        string       `json:"format"`
	Path          string       `json:"path"`
	Damaged       bool         `json:"damaged"`
	Damage        []DamageKind `json:"damage,omitempty"`
	Size          uint64       `json:"size"`
	SHA256        string       `json:"sha256"`
	Layout        *Layout      `json:"-"`
	Injections    []Injection  `json:"injections,omitempty"`
	TrackCount    int          `json:"track_count"`
	LogicalTracks []string     `json:"logical_tracks"`
}

// Set is the complete fixture set written to disk.
type Set struct {
	Dir       string
	Artifacts []*Artifact
}

// Manifest returns the deterministic JSON manifest of everything written.
func (s *Set) Manifest() ([]byte, error) {
	type entry struct {
		Spec          string       `json:"spec"`
		Format        string       `json:"format"`
		File          string       `json:"file"`
		Damaged       bool         `json:"damaged"`
		Damage        []DamageKind `json:"damage,omitempty"`
		Size          uint64       `json:"size_bytes"`
		SHA256        string       `json:"sha256"`
		TrackCount    int          `json:"track_count"`
		LogicalTracks []string     `json:"logical_tracks"`
		Injections    []Injection  `json:"injections,omitempty"`
	}
	var entries []entry
	for _, a := range s.Artifacts {
		entries = append(entries, entry{
			Spec: a.Spec, Format: a.Format, File: filepath.Base(a.Path),
			Damaged: a.Damaged, Damage: a.Damage, Size: a.Size, SHA256: a.SHA256,
			TrackCount: a.TrackCount, LogicalTracks: a.LogicalTracks, Injections: a.Injections,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].File < entries[j].File })
	return json.MarshalIndent(entries, "", "  ")
}

// Clean returns the artifact for a spec's intact file.
func (s *Set) Clean(specName string) *Artifact {
	for _, a := range s.Artifacts {
		if a.Spec == specName && !a.Damaged {
			return a
		}
	}
	return nil
}

// Damaged returns the damaged artifact for a spec, if one was requested.
func (s *Set) Damaged(specName string) *Artifact {
	for _, a := range s.Artifacts {
		if a.Spec == specName && a.Damaged {
			return a
		}
	}
	return nil
}

// Generate writes intact samples, one damaged copy per spec and the manifest
// into dir. Damage kinds that need a different box tree (index) are applied by
// rebuilding the spec before the byte-level injections.
func Generate(dir string, specs []Spec, kinds []DamageKind) (*Set, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	set := &Set{Dir: dir}
	for _, spec := range specs {
		layout, data, err := build(spec)
		if err != nil {
			return nil, err
		}
		clean, err := writeArtifact(dir, spec, layout, data, false, nil, nil)
		if err != nil {
			return nil, err
		}
		set.Artifacts = append(set.Artifacts, clean)

		if len(kinds) == 0 {
			continue
		}
		damagedSpec := spec
		dmgKinds := append([]DamageKind(nil), kinds...)
		if containsKind(kinds, DamageIndex) {
			damagedSpec = dropIndexes(spec)
		}
		dmgLayout, dmgData, err := build(damagedSpec)
		if err != nil {
			return nil, err
		}
		byteKinds := make([]DamageKind, 0, len(dmgKinds))
		for _, k := range dmgKinds {
			if k != DamageIndex {
				byteKinds = append(byteKinds, k)
			}
		}
		plan, _, err := Plan(dmgLayout, byteKinds)
		if err != nil {
			return nil, err
		}
		damaged, injections, err := Apply(dmgLayout, dmgData, plan)
		if err != nil {
			return nil, err
		}
		// Re-map the injection offsets onto the rebuilt layout so their
		// reported ranges describe the same frames the clean file has.
		artifact, err := writeArtifact(dir, spec, dmgLayout, damaged, true, dmgKinds, injections)
		if err != nil {
			return nil, err
		}
		set.Artifacts = append(set.Artifacts, artifact)
	}
	manifest, err := set.Manifest()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o644); err != nil {
		return nil, err
	}
	return set, nil
}

func containsKind(kinds []DamageKind, k DamageKind) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

func dropIndexes(s Spec) Spec {
	out := s
	out.Tracks = append([]TrackSpec(nil), s.Tracks...)
	for i := range out.Tracks {
		out.Tracks[i].DropsIndex = true
	}
	return out
}

func build(s Spec) (*Layout, []byte, error) {
	switch s.Format {
	case model.ContainerMP4:
		return BuildMP4(s)
	case model.ContainerMXF:
		return BuildMXF(s)
	default:
		return nil, nil, fmt.Errorf("fixture: unsupported format %q", s.Format)
	}
}

func writeArtifact(dir string, spec Spec, layout *Layout, data []byte, damaged bool, kinds []DamageKind, injections []Injection) (*Artifact, error) {
	name := spec.Name + "." + string(spec.Format)
	if damaged {
		name = spec.Name + ".damaged." + string(spec.Format)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	logical := make([]string, 0, len(layout.Tracks))
	for _, tl := range layout.Tracks {
		logical = append(logical, tl.Spec.Logical)
	}
	sort.Strings(logical)
	return &Artifact{
		Spec: spec.Name, Format: string(spec.Format), Path: path,
		Damaged: damaged, Damage: kinds, Size: uint64(len(data)),
		SHA256: hex.EncodeToString(sum[:]), Layout: layout,
		Injections: injections, TrackCount: len(logical), LogicalTracks: logical,
	}, nil
}
