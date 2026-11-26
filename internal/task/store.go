package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"assetcheck/internal/cache"
	"assetcheck/internal/manifest"
)

type Stage string

const (
	StageDiscovered Stage = "discovered"
	StageParsed     Stage = "parsed"
	StageMeasured   Stage = "measured"
	StageMerged     Stage = "merged"
	StageDone       Stage = "done"
)

var StageOrder = []Stage{
	StageDiscovered,
	StageParsed,
	StageMeasured,
	StageMerged,
	StageDone,
}

var ErrInterrupted = errors.New("simulated interruption after safe checkpoint")

func StageRank(s Stage) int {
	for i, candidate := range StageOrder {
		if candidate == s {
			return i
		}
	}
	return -1
}

// StableKey exposes the required four-part delivery-independent identity.
func StableKey(assetID, language, trackType, contentDigest string) string {
	return manifest.StableKey(assetID, language, trackType, contentDigest)
}

// BundleStableKey deterministically combines the language/track identities in
// one multi-track delivery. Delivery count is not an input.
func BundleStableKey(assetID string, languages, trackTypes []string, contentDigest string) string {
	return manifest.StableKey(assetID, "languages="+joinSorted(languages), "types="+joinSorted(trackTypes), contentDigest)
}

func joinSorted(values []string) string {
	cp := append([]string(nil), values...)
	sortStrings(cp)
	out := ""
	for i, v := range cp {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

type State struct {
	StableKey      string             `json:"stable_key"`
	AssetID        string             `json:"asset_id"`
	SourcePath     string             `json:"source_path"`
	InputSHA256    string             `json:"input_sha256"`
	InputIdentity  cache.FileIdentity `json:"input_identity"`
	Stage          Stage              `json:"stage"`
	ResumeFrom     Stage              `json:"resume_from,omitempty"`
	StructurePath  string             `json:"structure_path,omitempty"`
	ReportPath     string             `json:"report_path,omitempty"`
	ManifestPath   string             `json:"manifest_path,omitempty"`
	ManifestSHA256 string             `json:"manifest_sha256,omitempty"`
	Committed      bool               `json:"committed"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

type Store struct {
	Dir string
}

func NewStore(workDir string) *Store {
	return &Store{Dir: filepath.Join(workDir, "tasks")}
}

func (s *Store) paths(key string) (state, structure, report string) {
	base := filepath.Join(s.Dir, key)
	return base + ".state.json", base + ".structure.json", base + ".report.json"
}

func (s *Store) Load(key string) (*State, error) {
	statePath, _, _ := s.paths(key)
	b, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) SaveState(st *State) error {
	if st.CreatedAt.IsZero() {
		st.CreatedAt = time.Now().UTC()
	}
	st.UpdatedAt = time.Now().UTC()
	statePath, _, _ := s.paths(st.StableKey)
	return cache.SaveJSONAtomic(statePath, st, 0o644)
}

func (s *Store) SaveStructure(key string, doc any) (string, error) {
	_, p, _ := s.paths(key)
	return p, cache.SaveJSONAtomic(p, doc, 0o644)
}

func (s *Store) LoadStructure(key string, dst any) (bool, error) {
	_, p, _ := s.paths(key)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) SaveReport(key string, report any) (string, error) {
	_, _, p := s.paths(key)
	return p, cache.SaveJSONAtomic(p, report, 0o644)
}

func (s *Store) LoadReport(key string, dst any) (bool, error) {
	_, _, p := s.paths(key)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return false, err
	}
	return true, nil
}

type Lock struct {
	f *os.File
}

func (s *Store) AcquireLock(key string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Join(s.Dir, "locks"), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "locks", key+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
