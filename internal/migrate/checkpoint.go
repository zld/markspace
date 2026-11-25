// Package migrate performs the three-stage manifest migration (parse,
// normalize, write) with atomic checkpoints so an interrupted run can resume or
// roll back without ever overwriting the last valid output.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"example.com/assetcheck/internal/timeline"
)

// checkpointDir is where per-stage checkpoints live inside the output root.
const checkpointDir = ".assetcheck/checkpoints"

// currentFile names the file that points at the committed generation.
const currentFile = "CURRENT"

// Checkpoint is one durable stage record. It is JSON and never contains a wall
// clock time, so a resumed run produces byte-identical output.
type Checkpoint struct {
	Stage           string `json:"stage"`
	Index           int    `json:"index"`
	InputDigest     string `json:"input_digest"`
	ManifestDigest  string `json:"manifest_digest"`
	ManifestVersion int    `json:"manifest_version"`
	OutputDigest    string `json:"output_digest"`
	// SummaryDigest binds the checkpoint to the exact stage record it covers.
	SummaryDigest string `json:"summary_digest"`
	// Failure carries the stage and offset of a failed write.
	Failure *Failure `json:"failure,omitempty"`
}

// Failure records where a stage stopped so a report can reproduce it.
type Failure struct {
	Stage     string `json:"stage"`
	Category  string `json:"category"`
	ElementID uint32 `json:"element_id,omitempty"`
	Offset    int64  `json:"offset"`
	Reason    string `json:"reason"`
}

// Store manages checkpoints and the atomic CURRENT pointer under one output
// root.
type Store struct {
	Root string
}

// NewStore prepares the checkpoint tree under root.
func NewStore(root string) (*Store, error) {
	dir := filepath.Join(root, checkpointDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, timeline.Wrap(err, timeline.StageResume, timeline.CatIO, 0, -1,
			"create checkpoint dir %s", dir)
	}
	return &Store{Root: root}, nil
}

func (s *Store) pathFor(cp Checkpoint) string {
	name := fmt.Sprintf("%02d-%s.json", cp.Index, cp.Stage)
	return filepath.Join(s.Root, checkpointDir, name)
}

// Save writes a checkpoint atomically: a temporary file in the same directory
// is fsynced and renamed over the final name. The record's own digest is
// computed over the JSON with SummaryDigest blank, so tampering or truncation
// is detectable on load.
func (s *Store) Save(cp Checkpoint) error {
	cp.SummaryDigest = ""
	payload, err := checkpointJSON(cp)
	if err != nil {
		return err
	}
	cp.SummaryDigest = digestBytes(payload)
	data, err := checkpointJSON(cp)
	if err != nil {
		return err
	}
	return atomicWrite(s.pathFor(cp), data)
}

// checkpointJSON renders the canonical on-disk form.
func checkpointJSON(cp Checkpoint) ([]byte, error) {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return nil, timeline.Wrap(err, timeline.StageResume, timeline.CatIO, 0, -1,
			"encode checkpoint")
	}
	return append(data, '\n'), nil
}

// LoadAll reads every checkpoint, ordered by stage index. Corrupt checkpoints
// are reported rather than skipped so a resume never silently loses a stage.
func (s *Store) LoadAll() ([]Checkpoint, error) {
	dir := filepath.Join(s.Root, checkpointDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, timeline.Wrap(err, timeline.StageResume, timeline.CatIO, 0, -1,
			"list checkpoints")
	}
	var out []Checkpoint
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, timeline.Wrap(err, timeline.StageResume, timeline.CatIO, 0, -1,
				"read checkpoint %s", e.Name())
		}
		var cp Checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			return nil, timeline.Wrap(err, timeline.StageResume, timeline.CatCheckpointState, 0, -1,
				"checkpoint %s is corrupt", e.Name())
		}
		sum := cp.SummaryDigest
		cp.SummaryDigest = ""
		payload, err := checkpointJSON(cp)
		if err != nil {
			return nil, err
		}
		if sum != digestBytes(payload) {
			return nil, timeline.New(timeline.StageResume, timeline.CatCheckpointState, 0, -1,
				"checkpoint %s failed its own digest check", e.Name())
		}
		cp.SummaryDigest = sum
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// Clear removes checkpoint files newer than keep (by index), used when a
// mismatched resume rolls back to a consistent point.
func (s *Store) Clear(newerThan int) error {
	all, err := s.LoadAll()
	if err != nil {
		return err
	}
	for _, cp := range all {
		if cp.Index <= newerThan {
			continue
		}
		if err := os.Remove(s.pathFor(cp)); err != nil && !os.IsNotExist(err) {
			return timeline.Wrap(err, timeline.StageResume, timeline.CatIO, 0, -1,
				"remove checkpoint %d", cp.Index)
		}
	}
	return nil
}

// Digest exposes the digest helper for tests and callers building checkpoints.
func Digest(data []byte) string { return digestBytes(data) }

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// atomicWrite writes data to a temporary sibling and renames it into place, so
// readers see either the old file or the new one, never a partial file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1,
			"create temp beside %s", path)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1,
			"write temp for %s", path)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1,
			"fsync temp for %s", path)
	}
	if err := tmp.Close(); err != nil {
		return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1,
			"close temp for %s", path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1,
			"rename %s into place", path)
	}
	return nil
}

// Current describes the committed generation.
type Current struct {
	Generation string `json:"generation"`
	Digest     string `json:"digest"`
}

// ReadCurrent returns the committed pointer, or a zero Current when none exists.
func (s *Store) ReadCurrent() (Current, error) {
	data, err := os.ReadFile(filepath.Join(s.Root, currentFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Current{}, nil
		}
		return Current{}, timeline.Wrap(err, timeline.StageResume, timeline.CatIO, 0, -1,
			"read CURRENT")
	}
	var cur Current
	if err := json.Unmarshal(data, &cur); err != nil {
		return Current{}, timeline.Wrap(err, timeline.StageResume, timeline.CatCheckpointState, 0, -1,
			"CURRENT is corrupt: %v", err)
	}
	return cur, nil
}

// CommitGeneration atomically points CURRENT at a fully written generation.
func (s *Store) CommitGeneration(cur Current) error {
	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1, "encode CURRENT")
	}
	data = append(data, '\n')
	return atomicWrite(filepath.Join(s.Root, currentFile), data)
}
