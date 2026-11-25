// Package archive writes the local, chunked manifest index and its
// checkpoints. Writes are deterministic and idempotent: indexing the same
// stage key twice never adds a row or changes an artifact hash, and an
// interrupted chunk is never mistaken for a committed one.
package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ManifestRow is one archived fingerprint record.
type ManifestRow struct {
	StageKey       string  `json:"stage_key"`
	Track          string  `json:"track"`
	Order          uint64  `json:"order"`
	FragmentSHA    string  `json:"fragment_sha256"`
	FrameSHA       string  `json:"frame_sha256"`
	SegmentSHA     string  `json:"segment_sha256"`
	PixelMean      float64 `json:"pixel_mean,omitempty"`
	PixelVariance  float64 `json:"pixel_variance,omitempty"`
	PeakDBFS       float64 `json:"peak_dbfs,omitempty"`
	AlignmentNs    int64   `json:"alignment_error_ns"`
	DTSNs          int64   `json:"dts_ns"`
	ContentDupe    bool    `json:"content_duplicate"`
	TimelineDistinct bool  `json:"timeline_distinct"`
}

// Chunk is one fully written manifest chunk file.
type Chunk struct {
	Index    int    `json:"index"`
	Path     string `json:"path"`
	RowCount int    `json:"row_count"`
	RowStart int    `json:"row_start"`
	RowEnd   int    `json:"row_end"`
	SHA256   string `json:"sha256"`
}

// Index is the chunked local manifest for one work directory.
type Index struct {
	mu       sync.Mutex
	dir      string
	seen     map[string]bool // stage keys already committed
	rows     []ManifestRow
	chunks   []Chunk
	chunkSize int
}

// OpenIndex opens (or creates) the index under workDir/archive. Existing
// committed chunks are read back so a resumed run starts from the last
// complete chunk without re-adding rows.
func OpenIndex(workDir string, chunkSize int) (*Index, error) {
	if chunkSize <= 0 {
		chunkSize = 8
	}
	dir := filepath.Join(workDir, "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	idx := &Index{
		dir:       dir,
		seen:      map[string]bool{},
		chunkSize: chunkSize,
	}
	if err := idx.loadCommitted(); err != nil {
		return nil, err
	}
	return idx, nil
}

// AddRow appends a row unless its stage key was already committed. It
// reports true when a new row was accepted, false on a duplicate delivery.
func (i *Index) AddRow(row ManifestRow) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.seen[row.StageKey] {
		return false
	}
	i.seen[row.StageKey] = true
	i.rows = append(i.rows, row)
	return true
}

// Rows returns a copy of all buffered plus committed rows in stable order.
func (i *Index) Rows() []ManifestRow {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]ManifestRow, len(i.rows))
	copy(out, i.rows)
	return out
}

// Len reports the total number of rows currently held.
func (i *Index) Len() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.rows)
}

// Chunks returns metadata for every committed chunk.
func (i *Index) Chunks() []Chunk {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]Chunk, len(i.chunks))
	copy(out, i.chunks)
	return out
}

// FlushChunk writes one chunk of up to chunkSize buffered rows. A chunk is
// first written to a temporary file and fsynced, then atomically renamed,
// and only afterward recorded in the checkpoint: a crash mid-write leaves
// no chunk that could be mistaken for committed.
func (i *Index) FlushChunk() (*Chunk, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	// All held rows are already covered by committed chunks: nothing to do.
	committed := 0
	if n := len(i.chunks); n > 0 {
		committed = i.chunks[n-1].RowEnd
	}
	if committed >= len(i.rows) {
		return nil, nil
	}
	start := committed
	end := start + i.chunkSize
	if end > len(i.rows) {
		end = len(i.rows)
	}
	chunkRows := i.rows[start:end]
	chunkIndex := len(i.chunks)
	chunk := Chunk{
		Index:    chunkIndex,
		RowStart: start,
		RowEnd:   end,
		RowCount: len(chunkRows),
		Path:     filepath.Join(i.dir, fmt.Sprintf("chunk-%04d.json", chunkIndex)),
	}
	data, err := json.MarshalIndent(chunkRows, "", "  ")
	if err != nil {
		return nil, err
	}
	sum := sha256Hex(data)
	chunk.SHA256 = sum
	if err := atomicWrite(chunk.Path, data); err != nil {
		return nil, err
	}
	i.chunks = append(i.chunks, chunk)
	if err := i.writeCheckpointLocked(); err != nil {
		return nil, err
	}
	return &chunk, nil
}

// FlushAll forces every buffered row into chunks and returns them.
func (i *Index) FlushAll() ([]Chunk, error) {
	var written []Chunk
	for {
		c, err := i.FlushChunk()
		if err != nil {
			return written, err
		}
		if c == nil {
			return written, nil
		}
		written = append(written, *c)
	}
}

// loadCommitted reads the checkpoint and rebuilds seen keys from the chunk
// files. Any chunk listed in the checkpoint is verified against its hash;
// a missing or mismatched chunk means the checkpoint is stale and ignored.
func (i *Index) loadCommitted() error {
	cp, err := readCheckpoint(i.dir)
	if err != nil {
		return err
	}
	if cp == nil {
		return nil
	}
	for _, ch := range cp.Chunks {
		data, err := os.ReadFile(ch.Path)
		if err != nil {
			// Stale checkpoint referencing an uncommitted/temp chunk.
			return nil
		}
		if sha256Hex(data) != ch.SHA256 {
			return nil
		}
		var rows []ManifestRow
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil
		}
		i.chunks = append(i.chunks, ch)
		for _, r := range rows {
			i.seen[r.StageKey] = true
			i.rows = append(i.rows, r)
		}
	}
	// Keep a deterministic row order across resumes.
	sort.SliceStable(i.rows, func(a, b int) bool {
		if i.rows[a].Track != i.rows[b].Track {
			return i.rows[a].Track < i.rows[b].Track
		}
		return i.rows[a].Order < i.rows[b].Order
	})
	return nil
}
