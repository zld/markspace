// Package batch plans time-bounded chunks, renders them through atomic
// temporary files, verifies their checksums, and commits resumable
// checkpoints to a local JSON index.
package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediad/internal/mederr"
)

// IndexName is the local checkpoint index inside an output directory.
const IndexName = "index.json"

// IndexVersion is the supported checkpoint schema version.
const IndexVersion = 1

// ChunkRecord is one committed chunk. The SHA256 covers the final output
// file and Fingerprint binds it to all processing parameters and code version.
type ChunkRecord struct {
	Index       int    `json:"index"`
	StartNs     int64  `json:"start_ns"`
	EndNs       int64  `json:"end_ns"`
	File        string `json:"file"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	Fingerprint string `json:"fingerprint"`
}

// Index is the atomic local checkpoint state.
type Index struct {
	Version     int           `json:"version"`
	Fingerprint string        `json:"fingerprint"`
	Chunks      []ChunkRecord `json:"chunks"`
}

// LoadReport describes deterministic recovery actions while loading an index.
type LoadReport struct {
	Missing         bool `json:"missing"`
	Corrupt         bool `json:"corrupt"`
	Rebuilt         bool `json:"rebuilt"`
	DuplicatesDedup int  `json:"duplicates_dedup"`
}

// LoadIndex reads and validates the checkpoint index. A missing index is not
// an error. A truncated/corrupt index is reported so the caller can rebuild
// incomplete chunks rather than guessing at its contents.
func LoadIndex(dir, fingerprint string) (*Index, LoadReport, error) {
	path := filepath.Join(dir, IndexName)
	data, err := os.ReadFile(path)
	rep := LoadReport{}
	if errors.Is(err, os.ErrNotExist) {
		rep.Missing = true
		return &Index{Version: IndexVersion, Fingerprint: fingerprint}, rep, nil
	}
	if err != nil {
		return nil, rep, mederr.New(mederr.CatIO, "index", -1, -1, false, "read index: %v", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil || idx.Version != IndexVersion {
		rep.Corrupt = true
		rep.Rebuilt = true
		// Preserve the invalid file for diagnosis, but never trust it.
		_ = os.WriteFile(path+".corrupt", data, 0o644)
		return &Index{Version: IndexVersion, Fingerprint: fingerprint}, rep, nil
	}
	if idx.Fingerprint != fingerprint {
		return nil, rep, mederr.New(mederr.CatIndex, "index", -1, -1, false,
			"parameter fingerprint mismatch: index=%s current=%s; refusing to overwrite unmatched results",
			idx.Fingerprint, fingerprint)
	}
	clean, dupes := dedupeChunks(idx.Chunks)
	idx.Chunks = clean
	rep.DuplicatesDedup = dupes
	return &idx, rep, nil
}

func dedupeChunks(in []ChunkRecord) ([]ChunkRecord, int) {
	byIndex := map[int]ChunkRecord{}
	dupes := 0
	conflict := map[int]bool{}
	for _, c := range in {
		old, ok := byIndex[c.Index]
		if !ok {
			byIndex[c.Index] = c
			continue
		}
		dupes++
		if old.SHA256 == c.SHA256 && old.File == c.File && old.EndNs == c.EndNs {
			continue // exact repeated commit is idempotent
		}
		conflict[c.Index] = true
		// Conflicting duplicates are not trusted; omit that chunk so it re-renders.
		delete(byIndex, c.Index)
	}
	out := make([]ChunkRecord, 0, len(byIndex))
	for _, c := range byIndex {
		if conflict[c.Index] {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, dupes
}

// Save atomically writes the index via a same-directory temporary file and rename.
func (idx *Index) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return mederr.New(mederr.CatIO, "index", -1, -1, false, "create output dir: %v", err)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return mederr.New(mederr.CatIndex, "index", -1, -1, false, "marshal index: %v", err)
	}
	data = append(data, '\n')
	final := filepath.Join(dir, IndexName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return mederr.New(mederr.CatIO, "index", -1, -1, false, "write temp index: %v", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return mederr.New(mederr.CatIO, "index", -1, -1, false, "commit index: %v", err)
	}
	return nil
}

// HashFile returns the SHA256 and byte count of path.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// FingerprintBytes returns a stable hex SHA256 fingerprint for canonical params.
func FingerprintBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
