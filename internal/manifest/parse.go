// Package manifest parses and validates local media pipeline manifests.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"medpipe/internal/apperr"
	"medpipe/internal/graph"
)

// Manifest is the normalized JSON description supplied to medpipe.
type Manifest struct {
	SchemaVersion int          `json:"schema_version"`
	Parameters    Parameters   `json:"parameters,omitempty"`
	Tracks        []Track      `json:"tracks"`
	Switches      []Switch     `json:"switches,omitempty"`
	Dependencies  []Dependency `json:"dependencies,omitempty"`
}

// Parameters are deterministic processing parameters included in the job key.
type Parameters struct {
	WrapBits     int `json:"wrap_bits,omitempty"`
	BatchSamples int `json:"batch_samples,omitempty"`
	BudgetUnits  int `json:"budget_units,omitempty"`
}

// Track identifies one local MP4/MOV source.
type Track struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	Kind          string `json:"kind,omitempty"`
	DurationTicks int64  `json:"duration_ticks,omitempty"`
}

// Switch describes a source change immediately before Sample (one-based).
type Switch struct {
	ID                string `json:"id,omitempty"`
	Track             string `json:"track"`
	Sample            int    `json:"sample"`
	LeftSource        string `json:"left_source,omitempty"`
	RightSource       string `json:"right_source,omitempty"`
	LeftAnchorOffset  *int64 `json:"left_anchor_offset_tick,omitempty"`
	RightAnchorOffset *int64 `json:"right_anchor_offset_tick,omitempty"`
}

// Dependency declares a logical stage and its requirements.
type Dependency struct {
	Stage    string   `json:"stage"`
	Requires []string `json:"requires,omitempty"`
}

// Load reads, parses and validates a manifest and its local paths.
func Load(path string) (*Manifest, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", apperr.New("E_MANIFEST_READ", "read manifest: %v", err).
			WithStage("manifest").WithField(path)
	}
	m, err := Parse(data, filepath.Dir(path), true)
	if err != nil {
		return nil, "", err
	}
	hash, err := m.CanonicalHash()
	if err != nil {
		return nil, "", err
	}
	return m, hash, nil
}

// Parse decodes and validates manifest JSON.  When checkFiles is true, paths
// must exist and remain inside baseDir.
func Parse(data []byte, baseDir string, checkFiles bool) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		offset := int64(0)
		if se, ok := err.(*json.SyntaxError); ok {
			offset = se.Offset
		}
		return nil, apperr.New("E_MANIFEST_JSON", "invalid manifest JSON: %v", err).
			WithStage("manifest").WithSample(0, offset)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = 1
	}
	if m.SchemaVersion != 1 {
		return nil, apperr.New("E_MANIFEST_VERSION", "unsupported schema_version=%d", m.SchemaVersion).
			WithStage("manifest").WithField("schema_version")
	}
	if len(m.Tracks) == 0 {
		return nil, apperr.New("E_MANIFEST_TRACKS", "at least one track is required").
			WithStage("manifest").WithField("tracks")
	}
	if m.Parameters.WrapBits == 0 {
		m.Parameters.WrapBits = 32
	}
	if m.Parameters.WrapBits != 32 {
		return nil, apperr.New("E_MANIFEST_PARAMETER", "only 32-bit RTP wrap is supported").
			WithStage("manifest").WithField("parameters.wrap_bits")
	}
	if m.Parameters.BatchSamples == 0 {
		m.Parameters.BatchSamples = 4
	}

	ids := map[string]bool{}
	for i := range m.Tracks {
		t := &m.Tracks[i]
		field := fmt.Sprintf("tracks[%d]", i)
		if strings.TrimSpace(t.ID) == "" {
			return nil, fieldErr("E_TRACK_MISSING", "track id is empty", field+".id")
		}
		if ids[t.ID] {
			return nil, fieldErr("E_TRACK_DUPLICATE", "duplicate track id %q", field+".id", t.ID)
		}
		ids[t.ID] = true
		if t.DurationTicks < 0 {
			return nil, fieldErr("E_MANIFEST_DURATION", "negative duration %d", field+".duration_ticks", t.DurationTicks)
		}
		rel, err := SafeMediaPath(baseDir, t.Path, checkFiles)
		if err != nil {
			return nil, err.WithTrack(t.ID, t.Path)
		}
		t.Path = filepath.ToSlash(rel)
	}

	for i, sw := range m.Switches {
		field := fmt.Sprintf("switches[%d]", i)
		if !ids[sw.Track] {
			return nil, fieldErr("E_TRACK_MISSING", "switch %s references unknown track %q", field+".track", sw.Track)
		}
		if sw.Sample <= 0 {
			return nil, fieldErr("E_MANIFEST_SAMPLE", "switch sample must be one-based and positive", field+".sample")
		}
	}

	nodes := make([]graph.Node, 0, len(m.Tracks)+len(m.Dependencies))
	for _, t := range m.Tracks {
		nodes = append(nodes, graph.Node{ID: t.ID})
	}
	stageIDs := map[string]bool{}
	for i, d := range m.Dependencies {
		field := fmt.Sprintf("dependencies[%d]", i)
		if strings.TrimSpace(d.Stage) == "" {
			return nil, fieldErr("E_MANIFEST_STAGE", "dependency stage is empty", field+".stage")
		}
		if ids[d.Stage] || stageIDs[d.Stage] {
			return nil, fieldErr("E_MANIFEST_STAGE", "duplicate stage id %q", field+".stage", d.Stage)
		}
		stageIDs[d.Stage] = true
		nodes = append(nodes, graph.Node{ID: d.Stage, Requires: d.Requires})
	}
	if _, err := graph.Build(nodes); err != nil {
		return nil, err
	}
	return &m, nil
}

// SafeMediaPath rejects absolute paths and parent traversal.  The returned
// path is relative to baseDir using slash separators.
func SafeMediaPath(baseDir, p string, checkFile bool) (string, *apperr.E) {
	if strings.TrimSpace(p) == "" {
		return "", apperr.New("E_PATH_EMPTY", "media path is empty").WithStage("manifest").WithField("path")
	}
	if filepath.IsAbs(p) {
		return "", apperr.New("E_PATH_ESCAPE", "absolute media paths are not allowed: %s", p).
			WithStage("manifest").WithField("path")
	}
	ext := strings.ToLower(filepath.Ext(p))
	if ext != ".mp4" && ext != ".mov" {
		return "", apperr.New("E_PATH_TYPE", "only .mp4 and .mov are allowed: %s", p).
			WithStage("manifest").WithField("path")
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", apperr.New("E_PATH_ESCAPE", "media path escapes manifest directory: %s", p).
			WithStage("manifest").WithField("path")
	}
	abs := filepath.Join(baseDir, clean)
	realBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", apperr.New("E_PATH_ESCAPE", "resolve base: %v", err).WithStage("manifest")
	}
	if checkFile {
		realAbs, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", apperr.New("E_TRACK_FILE_MISSING", "media file unavailable %s: %v", p, err).
				WithStage("manifest").WithField("path")
		}
		realBaseEval, err := filepath.EvalSymlinks(realBase)
		if err != nil {
			return "", apperr.New("E_PATH_ESCAPE", "resolve manifest directory: %v", err).WithStage("manifest")
		}
		rel, err := filepath.Rel(realBaseEval, realAbs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", apperr.New("E_PATH_ESCAPE", "symlink resolves outside manifest directory: %s", p).
				WithStage("manifest").WithField("path")
		}
		st, err := os.Stat(realAbs)
		if err != nil || st.IsDir() {
			return "", apperr.New("E_TRACK_FILE_MISSING", "media file is not a regular file: %s", p).
				WithStage("manifest").WithField("path")
		}
	}
	rel, err := filepath.Rel(realBase, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", apperr.New("E_PATH_ESCAPE", "media path escapes manifest directory: %s", p).
			WithStage("manifest").WithField("path")
	}
	return filepath.ToSlash(rel), nil
}

// CanonicalJSON returns stable normalized manifest bytes.
func (m *Manifest) CanonicalJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, apperr.New("E_MANIFEST_HASH", "canonical encode: %v", err).WithStage("manifest")
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// CanonicalHash returns SHA-256 of CanonicalJSON.
func (m *Manifest) CanonicalHash() (string, error) {
	b, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// JobKey mixes manifest hash, sorted source hashes and run parameters.
func JobKey(manifestHash string, sourceHashes map[string]string, params Parameters) string {
	keys := make([]string, 0, len(sourceHashes))
	for k := range sourceHashes {
		keys = append(keys, k)
	}
	sortedHashes := make([][2]string, 0, len(keys))
	for _, k := range keys {
		sortedHashes = append(sortedHashes, [2]string{k, sourceHashes[k]})
	}
	keyMaterial, _ := json.Marshal(struct {
		Manifest string      `json:"manifest_sha256"`
		Sources  [][2]string `json:"sources"`
		Params   Parameters  `json:"parameters"`
	}{manifestHash, sortedHashes, params})
	sum := sha256.Sum256(keyMaterial)
	return hex.EncodeToString(sum[:])
}

func fieldErr(code, format, field string, args ...any) *apperr.E {
	return apperr.New(code, format, args...).WithStage("manifest").WithField(field)
}
