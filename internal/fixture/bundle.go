package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediakey/internal/model"
)

// On-disk bundle file names. Plaintexts live separately from the manifest so
// the manifest alone never carries media bytes.
const (
	ManifestFile   = "manifest.json"
	PlaintextsFile = "plaintexts.json"
	FaultsFile     = "faults.json"
)

type plaintextBundle struct {
	Plaintexts map[string][]byte `json:"plaintexts"`
}

// Save writes a sample bundle into dir, creating the directory if needed.
func (s *Sample) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create bundle dir: %w", err)
	}
	if err := writeJSON(filepath.Join(dir, ManifestFile), s.Manifest); err != nil {
		return err
	}
	texts := map[string][]byte{}
	for id, p := range s.Plaintexts {
		texts[fmt.Sprintf("%d", id)] = p
	}
	if err := writeJSON(filepath.Join(dir, PlaintextsFile), &plaintextBundle{Plaintexts: texts}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, FaultsFile), s.Faults); err != nil {
		return err
	}
	return nil
}

// Load reads a bundle previously written with Save. It recomputes the
// manifest digest and rejects a tampered manifest as a file error.
func Load(dir string) (*Sample, error) {
	var man model.Manifest
	if err := readJSON(filepath.Join(dir, ManifestFile), &man); err != nil {
		return nil, err
	}
	if man.Digest != "" && ManifestDigest(man) != man.Digest {
		return nil, fmt.Errorf("manifest digest verification failed for %s", dir)
	}
	var texts plaintextBundle
	if err := readJSON(filepath.Join(dir, PlaintextsFile), &texts); err != nil {
		return nil, err
	}
	faults := map[int]string{}
	var rawFaults map[string]string
	if err := readJSON(filepath.Join(dir, FaultsFile), &rawFaults); err != nil {
		return nil, err
	}
	for k, v := range rawFaults {
		var id int
		if _, err := fmt.Sscanf(k, "%d", &id); err != nil {
			return nil, fmt.Errorf("invalid fault key %q: %w", k, err)
		}
		faults[id] = v
	}
	plain := map[int][]byte{}
	for k, v := range texts.Plaintexts {
		var id int
		if _, err := fmt.Sscanf(k, "%d", &id); err != nil {
			return nil, fmt.Errorf("invalid plaintext key %q: %w", k, err)
		}
		plain[id] = v
	}
	return &Sample{Manifest: man, Plaintexts: plain, Faults: faults}, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
