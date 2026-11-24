// Package checkpoint persists chunk completion with SHA-256 verification and
// atomic index replacement. Corrupt indexes are rebuilt from verified chunks.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediarc/internal/model"
)

const indexVersion = 1

// Chunk is one completed output chunk.
type Chunk struct {
	Index      int    `json:"index"`
	StartFrame int    `json:"start_frame"`
	EndFrame   int    `json:"end_frame"` // exclusive
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
}

// Index is the atomically replaced local batch index.
type Index struct {
	Version      int     `json:"version"`
	Workdir      string  `json:"workdir,omitempty"`
	InputSHA256  string  `json:"input_sha256"`
	ParamsSHA256 string  `json:"params_sha256"`
	ChunkSize    int     `json:"chunk_size"`
	ChunkCount   int     `json:"chunk_count"`
	NextChunk    int     `json:"next_chunk"`
	Chunks       []Chunk `json:"chunks"`
	OutputPath   string  `json:"output_path,omitempty"`
	OutputSHA256 string  `json:"output_sha256,omitempty"`
	Completed    bool    `json:"completed"`
}

// Recovery explains startup checkpoint validation.
type Recovery struct {
	IndexExisted     bool   `json:"index_existed"`
	IndexCorrupt     bool   `json:"index_corrupt"`
	InputMismatch    bool   `json:"input_mismatch"`
	RolledBackFrom   int    `json:"rolled_back_from"`
	ResumedFromChunk int    `json:"resumed_from_chunk"`
	Reason           string `json:"reason,omitempty"`
}

// Store owns the workdir checkpoint layout.
type Store struct {
	Dir       string
	ChunkDir  string
	IndexPath string
}

// New creates paths but does not touch disk.
func New(workdir string) *Store {
	return &Store{Dir: workdir, ChunkDir: filepath.Join(workdir, "chunks"), IndexPath: filepath.Join(workdir, "index.json")}
}

// Prepare creates workdir and chunk directory.
func (s *Store) Prepare() error {
	if err := os.MkdirAll(s.ChunkDir, 0o755); err != nil {
		return err
	}
	return syncDir(s.Dir)
}

// ChunkPath returns the final chunk artifact path.
func (s *Store) ChunkPath(i int) string {
	return filepath.Join(s.ChunkDir, fmt.Sprintf("chunk-%06d.bin", i))
}

// HashBytes returns lowercase hex SHA-256.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile hashes a local file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := copySync(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func contiguousPrefix(chunks []Chunk) int {
	seen := map[int]bool{}
	for _, c := range chunks {
		seen[c.Index] = true
	}
	n := 0
	for seen[n] {
		n++
	}
	return n
}

// Save atomically writes and fsyncs the JSON index.
func (s *Store) Save(idx *Index) error {
	if err := s.Prepare(); err != nil {
		return err
	}
	idx.Version = indexVersion
	sort.SliceStable(idx.Chunks, func(i, j int) bool { return idx.Chunks[i].Index < idx.Chunks[j].Index })
	tmp := s.IndexPath + ".tmp"
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.IndexPath); err != nil {
		return err
	}
	return syncDir(s.Dir)
}

// CommitChunk writes, hashes, fsyncs and atomically renames one chunk, then
// records it in the index. A failed chunk never reaches idx.Chunks.
func (s *Store) CommitChunk(idx *Index, c Chunk, data []byte) error {
	if err := s.Prepare(); err != nil {
		return err
	}
	c.Bytes = int64(len(data))
	c.SHA256 = HashBytes(data)
	final := s.ChunkPath(c.Index)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	got, err := HashFile(tmp)
	if err != nil {
		return err
	}
	if got != c.SHA256 {
		_ = os.Remove(tmp)
		return model.NewExitError(1, "chunk hash verification failed", model.Fault{Kind: model.FaultChunkHash, Expected: c.SHA256, Actual: got})
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	sidecar := final + ".sha256"
	if err := os.WriteFile(sidecar, []byte(c.SHA256+"  "+filepath.Base(final)+"\n"), 0o644); err != nil {
		return err
	}
	_ = syncFile(sidecar)

	replaced := false
	for i, old := range idx.Chunks {
		if old.Index == c.Index {
			idx.Chunks[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		idx.Chunks = append(idx.Chunks, c)
	}
	idx.NextChunk = contiguousPrefix(idx.Chunks)
	return s.Save(idx)
}
