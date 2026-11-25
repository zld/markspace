// Package jobs persists idempotent task records and stage checkpoints under
// the requested local work directory.
package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/zld/media-check/internal/fault"
)

// JobRecord is the stable per-task state record. One file exists per TaskKey.
type JobRecord struct {
	TaskKey          string   `json:"task_key"`
	Scope            string   `json:"scope"`
	InputFingerprint string   `json:"input_fingerprint"`
	Status           string   `json:"status"`
	CompletedStages  []string `json:"completed_stages"`
	ManifestSHA256   string   `json:"manifest_sha256,omitempty"`
}

type Store struct {
	root string
}

func NewStore(workDir string) (*Store, error) {
	if workDir == "" {
		return nil, fault.E(fault.StageJobs, fault.CodeInvalidArgument, "work directory is required")
	}
	for _, dir := range []string{filepath.Join(workDir, "jobs"), filepath.Join(workDir, "checkpoints")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fault.Wrap(fault.StageJobs, fault.CodeReadFailed, err, "create state directory",
				fault.WithPath(dir))
		}
	}
	return &Store{root: workDir}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) jobPath(taskKey string) string {
	return filepath.Join(s.root, "jobs", taskKey+".json")
}

func (s *Store) checkpointPath(taskKey string) string {
	return filepath.Join(s.root, "checkpoints", taskKey+".json")
}

// SaveJob atomically upserts the one record for a stable task key.
func (s *Store) SaveJob(rec JobRecord) error {
	if rec.TaskKey == "" {
		return fault.E(fault.StageJobs, fault.CodeInvalidArgument, "task key is required")
	}
	if rec.CompletedStages == nil {
		rec.CompletedStages = []string{}
	} else {
		rec.CompletedStages = compactStages(rec.CompletedStages)
	}
	return writeJSONAtomic(s.jobPath(rec.TaskKey), rec)
}

func (s *Store) LoadJob(taskKey string) (*JobRecord, error) {
	var rec JobRecord
	if err := readJSON(s.jobPath(taskKey), &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) ListJobs() ([]JobRecord, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "jobs"))
	if err != nil {
		return nil, fault.Wrap(fault.StageJobs, fault.CodeReadFailed, err, "list jobs")
	}
	var out []JobRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var rec JobRecord
		if err := readJSON(filepath.Join(s.root, "jobs", entry.Name()), &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskKey < out[j].TaskKey })
	return out, nil
}

func compactStages(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, stage := range in {
		if stage == "" || seen[stage] {
			continue
		}
		seen[stage] = true
		out = append(out, stage)
	}
	return out
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.Wrap(fault.StageJobs, fault.CodeInvalidArgument, err, "encode JSON")
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fault.Wrap(fault.StageJobs, fault.CodeReadFailed, err, "create temporary state file",
			fault.WithPath(path))
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fault.Wrap(fault.StageJobs, fault.CodeReadFailed, err, "write state file", fault.WithPath(path))
	}
	if err := tmp.Close(); err != nil {
		return fault.Wrap(fault.StageJobs, fault.CodeReadFailed, err, "close state file", fault.WithPath(path))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fault.Wrap(fault.StageJobs, fault.CodeReadFailed, err, "replace state file", fault.WithPath(path))
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		code := fault.CodeReadFailed
		if os.IsNotExist(err) {
			code = fault.CodeCheckpointNotFound
		}
		return fault.Wrap(fault.StageJobs, code, err, "read state file", fault.WithPath(path))
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fault.Wrap(fault.StageJobs, fault.CodeInvalidArgument, err, "decode state file", fault.WithPath(path))
	}
	return nil
}
