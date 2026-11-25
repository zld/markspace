package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
)

const (
	ManifestFileName = "manifest.json"
	ManifestVersion  = 1
)

// ManifestSource identifies the immutable source for all derivatives.
type ManifestSource struct {
	Digest  string `json:"digest"`
	Fixture string `json:"fixture"`
	Mutated bool   `json:"mutated"`
}

// ManifestSegment is one independently deliverable MP4 fragment.
type ManifestSegment struct {
	Index       int               `json:"index"`
	ID          string            `json:"id"`
	File        string            `json:"file"`
	SHA256      string            `json:"sha256"`
	SizeBytes   int64             `json:"size_bytes"`
	Time        TimeRange         `json:"time"`
	ContentHash string            `json:"content_hash"`
	EventIDs    []string          `json:"event_ids,omitempty"`
	GapIDs      []string          `json:"gap_ids,omitempty"`
	Silence     []SilenceInterval `json:"silence,omitempty"`
}

// Manifest is the reviewable delivery checklist.
type Manifest struct {
	Version   int               `json:"version"`
	Source    ManifestSource    `json:"source"`
	Stages    []string          `json:"stages"`
	Wraps     int               `json:"wraps"`
	CutCount  int               `json:"cut_count"`
	SilenceMS int64             `json:"silence_ms"`
	GapEvents []GapEvent        `json:"gap_events"`
	Segments  []ManifestSegment `json:"segments"`
}

// ValidationReport lists every manifest verification failure.
type ValidationReport struct {
	Valid      bool     `json:"valid"`
	Checked    int      `json:"checked"`
	ErrorCount int      `json:"error_count"`
	Errors     []string `json:"errors,omitempty"`
}

// ManifestPath safely resolves manifest.json.
func ManifestPath(out string) (string, error) {
	return SafeJoin(out, ManifestFileName)
}

// WriteManifest atomically writes the bounded manifest.
func WriteManifest(out string, m *Manifest) (string, error) {
	m.Version = ManifestVersion
	path, err := ManifestPath(out)
	if err != nil {
		return "", err
	}
	if err := WriteJSONAtomic(path, m, MaxManifestBytes); err != nil {
		return path, err
	}
	return path, nil
}

// LoadManifest reads manifest.json.
func LoadManifest(out string) (*Manifest, string, error) {
	path, err := ManifestPath(out)
	if err != nil {
		return nil, "", err
	}
	var m Manifest
	if err := ReadJSONLimited(path, MaxManifestBytes, &m); err != nil {
		return nil, path, err
	}
	return &m, path, nil
}

// ValidateManifest checks hashes, ranges, ordering, and gap references.
func ValidateManifest(out string, m *Manifest) ValidationReport {
	rep := ValidationReport{Valid: true}
	addError := func(format string, args ...any) {
		rep.Valid = false
		rep.ErrorCount++
		rep.Errors = append(rep.Errors, fmt.Sprintf(format, args...))
	}
	if m == nil {
		addError("manifest is nil")
		return rep
	}
	if m.Source.Digest == "" {
		addError("source digest is missing")
	}
	gapIDs := map[string]struct{}{}
	for _, g := range m.GapEvents {
		if g.ID == "" {
			addError("gap event with empty id")
			continue
		}
		gapIDs[g.ID] = struct{}{}
		if g.Time.End90k < g.Time.Start90k {
			addError("gap %s has reversed time range", g.ID)
		}
	}

	var lastEnd uint64
	for i, s := range m.Segments {
		rep.Checked++
		if s.ID == "" || s.File == "" {
			addError("segment %d lacks id/file", i)
			continue
		}
		if s.Time.End90k <= s.Time.Start90k {
			addError("segment %s has invalid time range %d-%d", s.ID, s.Time.Start90k, s.Time.End90k)
		}
		if i > 0 && s.Time.Start90k < lastEnd {
			addError("segment %s overlaps or goes backwards before %d", s.ID, lastEnd)
		}
		lastEnd = s.Time.End90k

		for _, gapID := range s.GapIDs {
			if _, ok := gapIDs[gapID]; !ok {
				addError("segment %s references missing gap %s", s.ID, gapID)
			}
		}
		for _, sil := range s.Silence {
			if _, ok := sil.Time.Intersect(s.Time); !ok && sil.Time.Duration90k() > 0 {
				addError("segment %s silence outside segment range", s.ID)
			}
		}

		path, err := SafeJoin(out, s.File)
		if err != nil {
			addError("segment %s unsafe path: %v", s.ID, err)
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			addError("segment %s missing file: %v", s.ID, err)
			continue
		}
		if !info.Mode().IsRegular() {
			addError("segment %s is not a regular file", s.ID)
		}
		if s.SizeBytes != info.Size() {
			addError("segment %s size manifest=%d disk=%d", s.ID, s.SizeBytes, info.Size())
		}
		hash, size, err := hashFile(path)
		if err != nil {
			addError("segment %s unreadable: %v", s.ID, err)
			continue
		}
		if s.SizeBytes != size {
			addError("segment %s hashed size mismatch %d != %d", s.ID, s.SizeBytes, size)
		}
		if s.SHA256 != hash {
			addError("segment %s sha256 mismatch manifest=%s disk=%s", s.ID, s.SHA256, hash)
		}
	}
	sort.Strings(rep.Errors)
	return rep
}

// ValidateManifestFile loads and validates the on-disk manifest.
func ValidateManifestFile(out string) (*Manifest, ValidationReport, string, error) {
	m, path, err := LoadManifest(out)
	if err != nil {
		return nil, ValidationReport{Valid: false, ErrorCount: 1, Errors: []string{err.Error()}}, path, err
	}
	return m, ValidateManifest(out, m), path, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
