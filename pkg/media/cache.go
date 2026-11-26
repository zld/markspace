package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WriteFileAtomic streams r into a temporary file in the destination directory
// and renames it only after close/sync. Readers therefore never observe a
// half-written derivative.
func WriteFileAtomic(path string, r io.Reader) (err error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if path, err = filepath.Abs(path); err != nil {
			return NewError(CatInvalidArgument, "", -1, -1, "resolve output path: %v", err)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = io.Copy(tmp, r); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Chmod(0o644); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

// SafeJoin joins name to root and rejects absolute names or parent traversal.
func SafeJoin(root, name string) (string, error) {
	if root == "" {
		return "", NewError(CatInvalidArgument, "", -1, -1, "empty output root")
	}
	if name == "" || strings.ContainsRune(name, '\x00') {
		return "", NewError(CatInvalidArgument, "", -1, -1, "empty or invalid file name")
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", NewError(CatInvalidArgument, "", -1, -1, "invalid root: %v", err)
	}
	cleanName := filepath.Clean(name)
	if filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", NewError(CatInvalidArgument, "", -1, -1, "file name escapes output root: %s", name)
	}
	out := filepath.Join(cleanRoot, cleanName)
	if out != cleanRoot && !strings.HasPrefix(out, cleanRoot+string(filepath.Separator)) {
		return "", NewError(CatInvalidArgument, "", -1, -1, "file name escapes output root: %s", name)
	}
	return out, nil
}

// WriteSafeJSON validates name under root and atomically writes JSON.
func WriteSafeJSON(root, name string, v any) (string, error) {
	p, err := SafeJoin(root, name)
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", NewError(CatInvalidEncoding, "", -1, -1, "encode JSON: %v", err)
	}
	if err := WriteFileAtomic(p, bytesReader(b)); err != nil {
		return "", err
	}
	return p, nil
}

func bytesReader(b []byte) io.Reader { return &byteReader{b: b} }

type byteReader struct {
	b []byte
	i int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// Cache is a content-addressed JSON derivative cache.
type Cache struct {
	root string
}

// NewCache opens or creates a derivative cache directory.
func NewCache(root string) (*Cache, error) {
	if root == "" {
		return nil, NewError(CatInvalidArgument, "", -1, -1, "cache root is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Cache{root: abs}, nil
}

// Key derives a stable cache key from a namespace and content digests.
func (c *Cache) Key(namespace string, parts ...string) string {
	h := sha256.New()
	_, _ = io.WriteString(h, namespace)
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, p)
	}
	return namespace + "-" + hex.EncodeToString(h.Sum(nil))[:24] + ".json"
}

// Path safely resolves a cache key.
func (c *Cache) Path(key string) (string, error) {
	return SafeJoin(c.root, key)
}

// GetJSON returns true when a cache entry exists and decodes.
func (c *Cache) GetJSON(key string, v any) (bool, error) {
	p, err := c.Path(key)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, NewError(CatInvalidEncoding, "", -1, -1, "decode cache entry %s: %v", key, err)
	}
	return true, nil
}

// PutJSON atomically stores a cache entry.
func (c *Cache) PutJSON(key string, v any) error {
	p, err := c.Path(key)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return NewError(CatInvalidEncoding, "", -1, -1, "encode cache entry: %v", err)
	}
	return WriteFileAtomic(p, bytesReader(b))
}

// CacheState is the explicit three-domain invalidation result.
type CacheState struct {
	StreamParse   string   `json:"stream_parse"`
	OffsetMapping string   `json:"offset_mapping"`
	TimelineIndex string   `json:"timeline_index"`
	Reasons       []string `json:"reasons"`
}

// ClassifyFingerprintChange implements the cache sharing rules:
//   - same stream digest: elementary-stream parse cache is reusable;
//   - changed container digest invalidates byte-offset mapping;
//   - changed normalized metadata invalidates timeline/related indexes;
//   - changed stream invalidates stream-derived offsets and timeline indexes.
func ClassifyFingerprintChange(oldFP, newFP Fingerprint) CacheState {
	st := CacheState{StreamParse: "hit", OffsetMapping: "valid", TimelineIndex: "valid"}
	if oldFP.StreamSHA256 != newFP.StreamSHA256 {
		st.StreamParse = "miss"
		st.OffsetMapping = "invalid"
		st.TimelineIndex = "invalid"
		st.Reasons = append(st.Reasons, "elementary stream digest changed")
	}
	if oldFP.ContainerSHA256 != newFP.ContainerSHA256 && st.OffsetMapping == "valid" {
		st.OffsetMapping = "invalid"
		st.Reasons = append(st.Reasons, "container digest changed; stream parse retained")
	}
	if oldFP.MetadataSHA256 != newFP.MetadataSHA256 {
		st.TimelineIndex = "invalid"
		st.Reasons = append(st.Reasons, "normalized track metadata changed")
	}
	return st
}

// CanonicalDigest returns a deterministic SHA-256 of JSON content. Structs are
// expected to use stable field ordering; Go's encoding/json sorts map keys.
func CanonicalDigest(v any) (string, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil, NewError(CatInvalidEncoding, "", -1, -1, "canonical encode: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}
