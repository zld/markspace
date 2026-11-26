package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ContentCache is keyed by full content SHA-256 and analyzer version. A path
// replacement with byte-identical content therefore still hits this cache.
type ContentCache struct {
	Dir string
}

func NewContentCache(dir string) *ContentCache {
	return &ContentCache{Dir: dir}
}

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *ContentCache) entryPath(sha256sum, analyzerVersion string) string {
	return filepath.Join(c.Dir, analyzerVersion, sha256sum+".json")
}

type contentEnvelope struct {
	SHA256          string          `json:"sha256"`
	AnalyzerVersion string          `json:"analyzer_version"`
	Payload         json.RawMessage `json:"payload"`
}

// Get unmarshals a cached payload. A missing or corrupt entry is a miss.
func (c *ContentCache) Get(sha256sum, analyzerVersion string, dst any) (bool, error) {
	b, err := os.ReadFile(c.entryPath(sha256sum, analyzerVersion))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var env contentEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return false, nil
	}
	if env.SHA256 != sha256sum || env.AnalyzerVersion != analyzerVersion {
		return false, nil
	}
	if dst != nil {
		if err := json.Unmarshal(env.Payload, dst); err != nil {
			return false, nil
		}
	}
	return true, nil
}

func (c *ContentCache) Put(sha256sum, analyzerVersion string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := contentEnvelope{SHA256: sha256sum, AnalyzerVersion: analyzerVersion, Payload: raw}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	path := c.entryPath(sha256sum, analyzerVersion)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".content-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	return os.Rename(name, path)
}

// Invalidate removes a stale content entry. Absence is not an error.
func (c *ContentCache) Invalidate(sha256sum, analyzerVersion string) error {
	if sha256sum == "" {
		return nil
	}
	err := os.Remove(c.entryPath(sha256sum, analyzerVersion))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
