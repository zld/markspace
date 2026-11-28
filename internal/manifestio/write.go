// Package manifestio serialises the artefact manifest with a canonical layout
// so two runs with identical artefacts produce identical bytes.
package manifestio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Write writes v as indented JSON via a temporary file and rename.
func Write(path string, v any) error {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	blob = append(blob, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return fmt.Errorf("create temp manifest: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
