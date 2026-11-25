package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/zld/mediacore/internal/trace"
)

// CoreVersion is the aggregate processing-kernel version.
const CoreVersion = "mediacore/v0.1.0"

// ModuleVersions isolates cache invalidation to the module whose algorithm
// changed.
var ModuleVersions = map[string]string{
	"model.timeline":   "model/v1",
	"ingest.mp4":       "mp4/v1.4.1-adapter-v1",
	"ingest.rtp":       "rtp/v1",
	"recover.index":    "recover/v1",
	"segment.cut":      "segment/v1",
	"audio.silence":    "silence/v1",
	"fingerprint.hash": "fingerprint/v1",
	"pipeline.summary": CoreVersion,
}

// Key is a derived-artifact cache key.
type Key struct {
	Module        string `json:"module"`
	ModuleVersion string `json:"module_version"`
	SourceDigest  string `json:"source_digest"`
	ParamsDigest  string `json:"params_digest"`
	Key           string `json:"key"`
}

// NewKey derives a key from source digest, exact processing parameters and
// module version.
func NewKey(module, sourceDigest string, params any) (Key, error) {
	version := ModuleVersions[module]
	if version == "" {
		version = CoreVersion
	}
	paramBytes, err := json.Marshal(params)
	if err != nil {
		return Key{}, trace.E(trace.CodeInvalidArgument, trace.StageDerived, "encode %s params: %v", module, err)
	}
	paramsSum := sha256.Sum256(paramBytes)
	paramsDigest := hex.EncodeToString(paramsSum[:])
	material := strings.Join([]string{module, version, sourceDigest, paramsDigest}, "\n")
	keySum := sha256.Sum256([]byte(material))
	return Key{
		Module: module, ModuleVersion: version, SourceDigest: sourceDigest,
		ParamsDigest: paramsDigest, Key: hex.EncodeToString(keySum[:]),
	}, nil
}

// CanonicalDigest returns a stable digest of JSON-serializable derived data.
func CanonicalDigest(v any) (string, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil, trace.E(trace.CodeInvalidArgument, trace.StageDerived, "canonical encode: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}

// Cache is a small JSON filesystem cache for derived artifacts.
type Cache struct {
	Dir string
}

// NewCache creates a cache directory.
func NewCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageDerived, "create cache dir: %v", err)
	}
	return &Cache{Dir: dir}, nil
}

// Path returns the JSON file path for key.
func (c *Cache) Path(key Key) string {
	name := strings.ReplaceAll(key.Module, ".", "_") + "-" + key.Key[:16] + ".json"
	return filepath.Join(c.Dir, name)
}

// StoreJSON persists value under key.
func (c *Cache) StoreJSON(key Key, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return trace.E(trace.CodeInvalidArgument, trace.StageDerived, "encode cache value: %v", err)
	}
	if err := os.WriteFile(c.Path(key), b, 0o644); err != nil {
		return trace.E(trace.CodeInvalidArgument, trace.StageDerived, "write cache value: %v", err)
	}
	return nil
}

// LoadJSON loads a cached value. ok=false means a miss.
func (c *Cache) LoadJSON(key Key, dst any) (bool, error) {
	b, err := os.ReadFile(c.Path(key))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, trace.E(trace.CodeInvalidArgument, trace.StageDerived, "read cache value: %v", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return false, trace.E(trace.CodeInvalidArgument, trace.StageDerived, "decode cache value: %v", err)
	}
	return true, nil
}
