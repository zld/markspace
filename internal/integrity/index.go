// Package integrity writes and verifies a SHA-256 index of the segment
// files referenced by published playlists. Verification re-reads every file
// from disk and can apply deterministic in-memory faults so the fixture
// scenario can reproduce checksum mismatches without mutating any file.
package integrity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"hlsflow/internal/config"
	"hlsflow/internal/safeio"
)

// Entry records one segment file's expected content hash and timing.
type Entry struct {
	Segment  int     `json:"segment"`
	File     string  `json:"file"`
	SHA256   string  `json:"sha256"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration"`
	// BoundarySeconds is the segment start on the shared cross-rendition
	// timeline, retained so verification can recompute drift from the index.
	BoundarySeconds float64 `json:"boundary_seconds"`
}

// RenditionIndex groups entries under a rendition name.
type RenditionIndex struct {
	Entries []Entry `json:"entries"`
}

// Index is the full on-disk checksum manifest.
type Index struct {
	Renditions map[string]RenditionIndex `json:"renditions"`
}

// IndexName is the published index file name.
const IndexName = "integrity.json"

// New returns an empty index.
func New() *Index {
	return &Index{Renditions: map[string]RenditionIndex{}}
}

// Add records one segment entry, replacing any prior entry for the file.
func (idx *Index) Add(rendition string, e Entry) {
	ri := idx.Renditions[rendition]
	for i := range ri.Entries {
		if ri.Entries[i].File == e.File {
			ri.Entries[i] = e
			idx.Renditions[rendition] = ri
			return
		}
	}
	ri.Entries = append(ri.Entries, e)
	idx.Renditions[rendition] = ri
}

// Save atomically writes the index inside root.
func (idx *Index) Save(root *safeio.Root) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return root.AtomicWrite(IndexName, append(data, '\n'))
}

// Load reads and parses the index from root.
func Load(root *safeio.Root) (*Index, error) {
	data, err := root.ReadFile(IndexName)
	if err != nil {
		return nil, fmt.Errorf("read integrity index: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse integrity index: %w", err)
	}
	return &idx, nil
}

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// xorReadSeeker wraps data and flips one byte while reading. It never touches
// the underlying bytes, so on-disk content stays intact.
type xorReadSeeker struct {
	data   []byte
	offset int
	off    int
	xor    byte
}

func (x *xorReadSeeker) Read(p []byte) (int, error) {
	if x.offset >= len(x.data) {
		return 0, io.EOF
	}
	n := copy(p, x.data[x.offset:])
	for i := 0; i < n; i++ {
		if x.offset+i == x.off {
			p[i] ^= x.xor
		}
	}
	x.offset += n
	if x.offset >= len(x.data) {
		return n, io.EOF
	}
	return n, nil
}

// OpenSegment opens a segment file, applying the job's configured fault for
// that rendition/segment when present. The returned content is in-memory only.
func OpenSegment(root *safeio.Root, job *config.Job, rendition, file string, segment int) ([]byte, error) {
	data, err := root.ReadFile(file)
	if err != nil {
		return nil, err
	}
	for i := range job.Faults {
		f := &job.Faults[i]
		if f.Rendition == rendition && f.Segment == segment && f.XOR != 0 && f.Offset < len(data) {
			r := &xorReadSeeker{data: data, off: f.Offset, xor: f.XOR}
			out := make([]byte, 0, len(data))
			buf := make([]byte, 4096)
			for {
				n, readErr := r.Read(buf)
				out = append(out, buf[:n]...)
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return nil, readErr
				}
			}
			return out, nil
		}
	}
	return data, nil
}

// HashReader hashes all readable bytes from r.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Equal is a small helper kept for test clarity.
func Equal(a, b []byte) bool { return bytes.Equal(a, b) }
