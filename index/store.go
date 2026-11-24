// Package index persists scan results bound to a cryptographic summary of the
// input. Any mismatch — changed bytes, incompatible version, interrupted write
// — invalidates the index and forces a rebuild.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediacheck/ebml"
	"mediacheck/timeline"
)

// IndexVersion is bumped when the on-disk record layout changes
// incompatibly. Older files are never interpreted.
const IndexVersion = 1

// MaxInputSize bounds accepted input files (512 MiB).
const MaxInputSize int64 = 512 << 20

// trailerMagic terminates every complete index file; its absence proves an
// interrupted write.
const trailerMagic = "MEDIACHECK-IDX-OK\n"

// Summary binds an index to one concrete input byte sequence.
type Summary struct {
	SHA256 [32]byte
	Size   int64
}

// Hex renders the digest as a lowercase hex string.
func (s Summary) Hex() string { return hex.EncodeToString(s.SHA256[:]) }

// BlockRecord is the persistent, payload-free projection of one block.
type BlockRecord struct {
	TrackNumber uint32 `json:"track"`
	TimeNs      int64  `json:"time_ns"`
	Offset      int64  `json:"offset"`
	Size        int64  `json:"size"`
	ClusterOff  int64  `json:"cluster_off"`
	CheckOK     bool   `json:"check_ok"`
}

// Record is the full persisted scan projection.
type Record struct {
	Version       int               `json:"version"`
	InputHex      string            `json:"input_sha256"`
	InputSize     int64             `json:"input_size"`
	TimeScaleNs   int64             `json:"time_scale_ns"`
	SegmentLive   bool              `json:"segment_live"`
	TruncatedAt   int64             `json:"truncated_at"`
	CheckFailures []int64           `json:"check_failures"`
	Tracks        []*ebml.TrackSpec `json:"tracks"`
	Blocks        []BlockRecord     `json:"blocks"`
}

// Store manages index files inside one directory.
type Store struct{ dir string }

// NewStore validates the directory and creates it when missing.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, timeline.New(timeline.CatConfig, "empty index directory")
	}
	if filepath.IsAbs(dir) && strings.Contains(dir, "..") {
		return nil, timeline.New(timeline.CatConfig, "path traversal in index directory")
	}
	clean := filepath.Clean(dir)
	if clean == "." || clean == string(filepath.Separator) {
		return nil, timeline.New(timeline.CatConfig, "refusing index root at filesystem boundary")
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return nil, timeline.New(timeline.CatIO, "cannot create index directory").Wrapf(err)
	}
	return &Store{dir: clean}, nil
}

// FromScan projects a scan result into a persistable record.
func FromScan(sum Summary, res *ebml.ScanResult) *Record {
	rec := &Record{
		Version:       IndexVersion,
		InputHex:      sum.Hex(),
		InputSize:     sum.Size,
		TimeScaleNs:   res.TimeScaleNs,
		SegmentLive:   res.SegmentLive,
		TruncatedAt:   res.TruncatedAt,
		CheckFailures: append([]int64(nil), res.CheckFailures...),
		Tracks:        res.Tracks,
	}
	for _, b := range res.Blocks {
		rec.Blocks = append(rec.Blocks, BlockRecord{
			TrackNumber: b.TrackNumber,
			TimeNs:      b.TimeNs,
			Offset:      b.Offset,
			Size:        b.Size,
			ClusterOff:  b.ClusterOff,
			CheckOK:     b.CheckOK,
		})
	}
	return rec
}

func (s *Store) pathFor(sum Summary) string {
	return filepath.Join(s.dir, sum.Hex()+".idx")
}

// Load returns the record only when the file is complete, version-compatible
// and bound to exactly this summary. The second return value reports why a
// rebuild is required ("" on a hit).
func (s *Store) Load(sum Summary) (*Record, string) {
	path := s.pathFor(sum)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "index missing"
	}
	if !strings.HasSuffix(string(raw), trailerMagic) {
		return nil, "index write was interrupted"
	}
	body := raw[:len(raw)-len(trailerMagic)]
	var rec Record
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, "index unreadable"
	}
	if rec.Version != IndexVersion {
		return nil, fmt.Sprintf("index version %d incompatible with %d", rec.Version, IndexVersion)
	}
	if rec.InputHex != sum.Hex() || rec.InputSize != sum.Size {
		return nil, "input summary changed"
	}
	return &rec, ""
}

// Save writes atomically: temp file, fsync, rename. A crash at any point
// leaves either the previous complete index or a .tmp file that Load ignores.
func (s *Store) Save(sum Summary, rec *Record) error {
	rec.Version = IndexVersion
	rec.InputHex = sum.Hex()
	rec.InputSize = sum.Size
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return timeline.New(timeline.CatIO, "index encode failed").Wrapf(err)
	}
	final := s.pathFor(sum)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return timeline.New(timeline.CatIO, "cannot open temp index").Wrapf(err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return timeline.New(timeline.CatIO, "temp index write failed").Wrapf(err)
	}
	if _, err := f.WriteString(trailerMagic); err != nil {
		f.Close()
		return timeline.New(timeline.CatIO, "trailer write failed").Wrapf(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return timeline.New(timeline.CatIO, "temp index fsync failed").Wrapf(err)
	}
	if err := f.Close(); err != nil {
		return timeline.New(timeline.CatIO, "temp index close failed").Wrapf(err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return timeline.New(timeline.CatIO, "index rename failed").Wrapf(err)
	}
	return nil
}

// DiscardTemp removes a leftover temp index so it can never be mistaken for a
// completed result.
func (s *Store) DiscardTemp(sum Summary) error {
	err := os.Remove(s.pathFor(sum) + ".tmp")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return timeline.New(timeline.CatIO, "temp index cleanup failed").Wrapf(err)
	}
	return nil
}

// Digest streams an input and returns its size-bounded summary.
func Digest(r io.Reader) (Summary, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, MaxInputSize+1))
	if err != nil {
		return Summary{}, timeline.New(timeline.CatIO, "summary read failed").Wrapf(err)
	}
	if n > MaxInputSize {
		return Summary{}, timeline.New(timeline.CatOverflow, "input exceeds size limit")
	}
	sum := Summary{Size: n}
	copy(sum.SHA256[:], h.Sum(nil))
	return sum, nil
}
