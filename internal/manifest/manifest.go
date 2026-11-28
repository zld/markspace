// Package manifest is the committed, self-verifying record of a conversion. It
// pins the input digest, the repair map, sample count, duration and bitrate,
// and the hash of every output, so a later run or an explicit verify can prove
// nothing drifted.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"avpipe/internal/config"
	"avpipe/internal/model"
	"avpipe/internal/repair"
	"avpipe/internal/scan"
)

// ManifestFile is the manifest's on-disk name inside the state directory.
const ManifestFile = "manifest.json"

// Manifest is the durable record verified across resume and repeated runs.
type Manifest struct {
	CaseName    string             `json:"case_name"`
	Digest      string             `json:"digest"`
	State       string             `json:"state"`
	SampleCount int                `json:"sample_count"`
	DurationMS  int                `json:"duration_ms"`
	Kbps        int                `json:"kbps"`
	Corrections []model.Correction `json:"corrections"`
	Outputs     map[string]string  `json:"outputs"`
}

// Build assembles the manifest from the exact values used for the run.
func Build(rep *repair.Report, cfg *config.Config, idx *scan.Index,
	outputs map[string]string) *Manifest {

	return &Manifest{
		CaseName:    idx.CaseName,
		Digest:      idx.Digest,
		State:       model.StateCommitted,
		SampleCount: cfg.KeptSamples,
		DurationMS:  cfg.DurationMS,
		Kbps:        cfg.Kbps,
		Corrections: append([]model.Correction(nil), rep.Corrections...),
		Outputs:     outputs,
	}
}

// Save writes the manifest atomically.
func (m *Manifest) Save(dir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return model.NewError(model.CodeIO, false, "marshal manifest: %v", err)
	}
	path := filepath.Join(dir, ManifestFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return model.NewError(model.CodeIO, false, "write manifest: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return model.NewError(model.CodeIO, false, "rename manifest: %v", err)
	}
	return nil
}

// Load reads a manifest from the state directory.
func Load(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, model.NewError(model.CodeIO, false, "read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, model.NewError(model.CodeManifest, false, "parse manifest: %v", err)
	}
	return &m, nil
}

// Check recomputes every pinned field from the current input and re-hashes the
// output files, returning the list of mismatches. An empty list means valid.
func (m *Manifest) Check(rep *repair.Report, cfg *config.Config, idx *scan.Index,
	stateDir string) []string {

	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	if m.Digest != idx.Digest {
		add("digest manifest=%s input=%s", m.Digest, idx.Digest)
	}
	if m.SampleCount != cfg.KeptSamples {
		add("sample_count manifest=%d measured=%d", m.SampleCount, cfg.KeptSamples)
	}
	if m.DurationMS != cfg.DurationMS {
		add("duration_ms manifest=%d measured=%d", m.DurationMS, cfg.DurationMS)
	}
	if m.Kbps != cfg.Kbps {
		add("kbps manifest=%d measured=%d", m.Kbps, cfg.Kbps)
	}
	if !reflect.DeepEqual(m.Corrections, rep.Corrections) {
		add("repair map differs from re-derived corrections")
	}
	for name, want := range m.Outputs {
		got, err := hashFile(filepath.Join(stateDir, name))
		if err != nil {
			add("output %s unreadable: %v", name, err)
			continue
		}
		if got != want {
			add("output %s hash manifest=%s now=%s", name, want, got)
		}
	}
	return problems
}

// Diff counts the number of top-level fields that differ between two
// manifests. Deterministic re-runs must report zero.
func Diff(a, b *Manifest) int {
	n := 0
	if a.Digest != b.Digest {
		n++
	}
	if a.State != b.State {
		n++
	}
	if a.SampleCount != b.SampleCount {
		n++
	}
	if a.DurationMS != b.DurationMS {
		n++
	}
	if a.Kbps != b.Kbps {
		n++
	}
	if !reflect.DeepEqual(a.Corrections, b.Corrections) {
		n++
	}
	if !reflect.DeepEqual(a.Outputs, b.Outputs) {
		n++
	}
	return n
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}
