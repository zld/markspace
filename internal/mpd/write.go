package mpd

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"dashgen/internal/manifest"
)

// xmlHeader is emitted explicitly because encoding/xml never writes one.
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

// Marshal serializes the MPD deterministically: struct field order fixes
// attribute and element order, encoding/xml escapes all text and attribute
// content, and two-space indentation keeps output readable and diff-stable.
func Marshal(doc *manifest.MPD) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("mpd: cannot marshal nil document")
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("mpd: marshal: %w", err)
	}
	return append([]byte(xmlHeader), body...), nil
}

// WriteFile writes the MPD atomically. Data is fully rendered to a temporary
// file in the destination directory and renamed into place only after a
// successful sync, so a write failure can never leave a half-written manifest
// at outPath.
func WriteFile(outPath string, doc *manifest.MPD) error {
	data, err := Marshal(doc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(outPath)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mpd: create output dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".dashgen-*.tmp")
	if err != nil {
		return fmt.Errorf("mpd: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("mpd: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("mpd: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("mpd: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		cleanup()
		return fmt.Errorf("mpd: rename into place: %w", err)
	}
	return nil
}
