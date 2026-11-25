package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	errs "mediad/internal/errors"
	"mediad/internal/stream"
)

// persistedSpec lets resume regenerate byte-identical fixture input.
type persistedSpec struct {
	FixtureID    string       `json:"fixture_id"`
	Codec        stream.Codec `json:"codec"`
	Frames       int          `json:"frames"`
	IndexMode    string       `json:"index_mode"`
	Truncate     int          `json:"truncate"`
	PayloadBytes int          `json:"payload_bytes"`
}

func writeSpec(dir string, spec persistedSpec) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "workdir: %v", err)
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return errs.New(errs.Fatal, errs.CodeInternal, "encode spec: %v", err)
	}
	tmp := filepath.Join(dir, "spec.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "write spec: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "spec.json")); err != nil {
		return errs.New(errs.Fatal, errs.CodeIOFailure, "rename spec: %v", err)
	}
	return nil
}

func readSpec(dir string) (persistedSpec, error) {
	var spec persistedSpec
	data, err := os.ReadFile(filepath.Join(dir, "spec.json"))
	if err != nil {
		return spec, errs.New(errs.Fatal, errs.CodeIOFailure, "read spec: %v", err)
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return spec, errs.New(errs.Fatal, errs.CodeIOFailure, "decode spec: %v", err)
	}
	return spec, nil
}

// resolveDir accepts either the exact fixture id or its codec prefix; the
// prefix form must match exactly one recorded fixture.
func resolveDir(root, ref string) (string, error) {
	exact := filepath.Join(root, ref)
	if info, err := os.Stat(filepath.Join(exact, "spec.json")); err == nil && !info.IsDir() {
		return exact, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", errs.New(errs.Fatal, errs.CodeIOFailure, "workdir scan: %v", err)
	}
	var matches []string
	for _, e := range entries {
		name := e.Name()
		if name == ref || (len(name) >= len(ref) && name[:len(ref)] == ref) {
			if _, err := os.Stat(filepath.Join(root, name, "spec.json")); err == nil {
				matches = append(matches, name)
			}
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return "", errs.New(errs.Fatal, errs.CodeCheckpointMismatch,
			"no fixture matching %q under %s", ref, root)
	case 1:
		return filepath.Join(root, matches[0]), nil
	default:
		return "", errs.New(errs.Fatal, errs.CodeCheckpointMismatch,
			"fixture prefix %q is ambiguous: %v", ref, matches)
	}
}
