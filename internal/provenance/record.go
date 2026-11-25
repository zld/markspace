// Package provenance records input digests, mapping versions, index digests and source lineage.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"mediacore/internal/index"
	"mediacore/internal/manifest"
)

const SchemaVersion = "provenance-v1"

// Artifact is one local input included in the root input digest.
type Artifact struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// SourceMapping links an index/result back to its manifest entry and input bytes.
type SourceMapping struct {
	StableKey        string `json:"stable_key"`
	System           string `json:"system"`
	ManifestPath     string `json:"manifest_path"`
	Representation   string `json:"representation"`
	EntryIndex       int    `json:"entry_index"`
	EntryNumber      int    `json:"entry_number"`
	OriginalSequence int    `json:"original_sequence"`
	InitPath         string `json:"init_path,omitempty"`
	MediaPath        string `json:"media_path"`
	ProgramStartU    int64  `json:"program_start_u"`
	ProgramEndU      int64  `json:"program_end_u"`
	InputSHA256      string `json:"media_sha256"`
	InitSHA256       string `json:"init_sha256,omitempty"`
	Aligned          bool   `json:"aligned"`
	DeviationU       int64  `json:"deviation_u"`
}

// Record is the stable per-output provenance document.
type Record struct {
	SchemaVersion      string        `json:"schema_version"`
	InputHash          string        `json:"input_hash"`
	CombinedInputHash  string        `json:"combined_input_hash"`
	ConfigHash         string        `json:"config_hash"`
	MappingRuleVersion string        `json:"mapping_rule_version"`
	IndexDigest        string        `json:"index_digest"`
	Source             SourceMapping `json:"source_mapping"`
}

// CombinedInputDigest hashes the manifest and every unique referenced media/init file.
func CombinedInputDigest(manifestPath string, entries []manifest.Entry) (string, []Artifact, error) {
	var artifacts []Artifact
	mh, size, err := index.HashFile(manifestPath)
	if err != nil {
		return "", nil, err
	}
	artifacts = append(artifacts, Artifact{Role: "manifest", Path: manifestPath, SHA256: mh, Size: size})
	seen := map[string]bool{manifestPath: true}
	for _, e := range entries {
		for _, item := range []struct{ role, path string }{{"init", e.InitPath}, {"media", e.MediaPath}} {
			if item.path == "" || seen[item.path] {
				continue
			}
			seen[item.path] = true
			h, n, err := index.HashFile(item.path)
			if err != nil {
				return "", nil, err
			}
			artifacts = append(artifacts, Artifact{Role: item.role, Path: item.path, SHA256: h, Size: n})
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Path == artifacts[j].Path {
			return artifacts[i].Role < artifacts[j].Role
		}
		return artifacts[i].Path < artifacts[j].Path
	})
	h := sha256.New()
	for _, a := range artifacts {
		h.Write([]byte(a.Role + "\n" + a.Path + "\n" + a.SHA256 + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), artifacts, nil
}

func ConfigDigest(config any) (string, error) {
	b, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func SourceFromEntry(e manifest.Entry, mediaHash, initHash string) SourceMapping {
	return SourceMapping{
		StableKey:        e.StableKey,
		System:           e.System,
		ManifestPath:     e.Manifest,
		Representation:   e.Representation,
		EntryIndex:       e.EntryIndex,
		EntryNumber:      e.EntryNumber,
		OriginalSequence: e.OriginalSequence,
		InitPath:         e.InitPath,
		MediaPath:        e.MediaPath,
		ProgramStartU:    e.ProgramStartU,
		ProgramEndU:      e.ProgramEndU,
		InputSHA256:      mediaHash,
		InitSHA256:       initHash,
		Aligned:          e.Aligned,
		DeviationU:       e.DeviationU,
	}
}

func NewRecord(combinedInputHash, configHash string, idx *index.EntryIndex) Record {
	return Record{
		SchemaVersion:      SchemaVersion,
		InputHash:          idx.InputSHA256,
		CombinedInputHash:  combinedInputHash,
		ConfigHash:         configHash,
		MappingRuleVersion: index.MappingRuleVersion,
		IndexDigest:        idx.Digest,
		Source: SourceMapping{
			StableKey:        idx.StableKey,
			System:           idx.System,
			ManifestPath:     idx.Manifest,
			Representation:   idx.Representation,
			EntryIndex:       idx.EntryIndex,
			EntryNumber:      idx.EntryNumber,
			OriginalSequence: idx.OriginalSequence,
			MediaPath:        idx.InputPath,
			ProgramStartU:    idx.ProgramStartU,
			ProgramEndU:      idx.ProgramEndU,
			InputSHA256:      idx.InputSHA256,
			InitSHA256:       idx.InitSHA256,
			Aligned:          idx.Aligned,
			DeviationU:       idx.DeviationU,
		},
	}
}

// WriteAtomicJSON writes JSON via a same-directory temp file and rename.
func WriteAtomicJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func FileKey(stableKey string) string {
	return safeName.ReplaceAllString(stableKey, "_")
}
