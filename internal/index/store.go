package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"media-audit/internal/source"
)

// Record is the searchable container index for one stable task key.
type Record struct {
	TaskKey         string `json:"task_key"`
	Path            string `json:"path"`
	ContentSHA256   string `json:"content_sha256"`
	StructureSHA256 string `json:"structure_sha256"`
	TrackCount      int    `json:"track_count"`
	ErrorCount      int    `json:"error_count"`
}

// Store invalidates by content or container structure. Metadata-only changes
// deliberately leave the structural index usable.
type Store struct {
	mu      sync.Mutex
	path    string
	Records map[string]Record `json:"records"`
}

func NewStore() *Store {
	return &Store{Records: make(map[string]Record)}
}

func Open(path string) (*Store, error) {
	s := NewStore()
	s.path = path
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		// A corrupt index is safe to rebuild; callers can log/report this.
		s = NewStore()
		s.path = path
	}
	return s, nil
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Upsert(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Records[r.TaskKey] = r
}

func (s *Store) Get(taskKey string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Records[taskKey]
	return r, ok
}

func (s *Store) IsValid(taskKey string, fp source.Fingerprint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Records[taskKey]
	return ok && r.ContentSHA256 == fp.ContentSHA256 && r.StructureSHA256 == fp.StructureSHA256
}

// Invalidate removes the index record when bytes or structure changed. It
// returns whether an index entry was removed.
func (s *Store) Invalidate(taskKey string, change source.ChangeSet) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !change.ContentReplaced && !change.StructureChanged {
		return false
	}
	if _, ok := s.Records[taskKey]; !ok {
		return false
	}
	delete(s.Records, taskKey)
	return true
}

func (s *Store) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.Records))
	for k := range s.Records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
