package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"archivecheck/internal/merr"
)

// nodeStore persists one JSON artifact per stable task key using
// temp-file + rename. It is what makes retries idempotent and lets an
// incremental resume reload committed artifacts without rerunning nodes.
type nodeStore struct {
	dir string
}

var safeKeyRE = regexp.MustCompile(`^[a-zA-Z0-9:._-]+$`)

func newNodeStore(outDir string) (*nodeStore, error) {
	dir := filepath.Join(outDir, ".archive", "nodes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, merr.Wrap(err, "pipeline", dir, "cannot create node store")
	}
	return &nodeStore{dir: dir}, nil
}

func (s *nodeStore) path(key string) (string, error) {
	if !safeKeyRE.MatchString(key) {
		return "", merr.New(merr.KindArgument, "pipeline", "node-store",
			"unsafe task key rejected: "+key)
	}
	// Keys never contain separators; escape the colon used in our scheme.
	escaped := make([]byte, 0, len(key))
	for i := 0; i < len(key); i++ {
		switch key[i] {
		case ':', '/', '\\':
			escaped = append(escaped, '_')
		default:
			escaped = append(escaped, key[i])
		}
	}
	return filepath.Join(s.dir, string(escaped)+".json"), nil
}

// Put atomically commits one node artifact.
func (s *nodeStore) Put(key string, v interface{}) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return merr.Wrap(err, "pipeline", key, "artifact encode failed")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return merr.Wrap(err, "pipeline", key, "artifact temp write failed")
	}
	if err := os.Rename(tmp, path); err != nil {
		return merr.Wrap(err, "pipeline", key, "artifact atomic commit failed")
	}
	return nil
}

// Has reports whether a committed artifact exists for key.
func (s *nodeStore) Has(key string) bool {
	path, err := s.path(key)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Get loads one committed artifact.
func (s *nodeStore) Get(key string, v interface{}) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return merr.Wrap(err, "pipeline", key, "artifact read failed")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return merr.Wrap(err, "pipeline", key, "artifact decode failed")
	}
	return nil
}

// LoadRecoveries loads every committed recovery artifact, in stable
// (track, sequence) order, so a resumed timeline sees the same inputs
// as a full rerun.
func (s *nodeStore) LoadRecoveries(keys []string) ([]*SegmentRecovery, error) {
	out := make([]*SegmentRecovery, 0, len(keys))
	for _, key := range keys {
		if !s.Has(key) {
			continue
		}
		var rec SegmentRecovery
		if err := s.Get(key, &rec); err != nil {
			return nil, err
		}
		out = append(out, &rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Spec.ID != out[j].Spec.ID {
			return out[i].Spec.ID < out[j].Spec.ID
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}
