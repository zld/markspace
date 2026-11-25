// Package index persists per-segment completion records and checkpoints.
// A checkpoint advances only after both the media segment and its index
// entry are on disk and verified; every write goes through a temp-file plus
// atomic rename so a crash can never leave a half-written index behind.
package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/clock"
	"mediad/internal/safeio"
)

// TrackIndex records how one target track was laid out inside a segment.
type TrackIndex struct {
	TrackID       int    `json:"track_id"`
	SourceChannel string `json:"source_channel"`
	Bytes         int    `json:"bytes"`
}

// Record is the durable description of one committed segment.
type Record struct {
	SegmentID   int             `json:"segment_id"`
	ChunkStart  int             `json:"chunk_start"`
	ChunkEnd    int             `json:"chunk_end"` // half-open
	FrameRange  clock.FrameRange `json:"frame_range"`
	FileName    string          `json:"file_name"`
	Keyframes   []uint64        `json:"keyframes"`
	Tracks      []TrackIndex    `json:"tracks"`
	Hash        string          `json:"hash"`
	Complete    bool            `json:"complete"`
}

// Index is the on-disk journal. It is marshaled as a single JSON document and
// replaced atomically on every commit.
type Index struct {
	Version     int       `json:"version"`
	SampleRate  uint32    `json:"sample_rate"`
	Records     []Record  `json:"records"`
	LastGoodID  int       `json:"last_good_segment_id"`
}

const (
	// Name is the fixed journal file name inside the output directory.
	Name = "index.json"
	version = 1
)

// New creates an empty journal for the given sample rate.
func New(sampleRate uint32) *Index {
	return &Index{Version: version, SampleRate: sampleRate, LastGoodID: -1}
}

// path joins the journal name inside root using the confined-path helper.
func path(root string) (string, error) {
	return safeio.SafeJoin(root, Name)
}

// Save validates, marshals and atomically replaces the journal in root.
func (ix *Index) Save(root string) error {
	if ix.Version != version {
		return fmt.Errorf("index: unsupported version %d", ix.Version)
	}
	if _, err := safeio.EnsureRoot(root); err != nil {
		return err
	}
	dst, err := path(root)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return safeio.AtomicWrite(dst, data)
}

// Load reads and parses the journal. A truncated or malformed file is reported
// as a checkpoint-corruption fault so the recovery layer can roll back to the
// last valid snapshot instead of skipping the damaged region.
func Load(root string) (*Index, error) {
	dst, err := path(root)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(dst)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, newCorrupt(err)
	}
	if ix.Version != version {
		return nil, newCorrupt(fmt.Errorf("index version %d unsupported", ix.Version))
	}
	if !ix.consistent() {
		return nil, newCorrupt(fmt.Errorf("index records are not contiguous or marked inconsistently"))
	}
	return &ix, nil
}

// consistent verifies that complete records are contiguous, ordered and that
// LastGoodID points at the final complete record.
func (ix *Index) consistent() bool {
	lastComplete := -1
	for i, r := range ix.Records {
		if r.SegmentID != i {
			return false
		}
		if r.Complete {
			if r.SegmentID != lastComplete+1 {
				return false
			}
			lastComplete = r.SegmentID
		} else if lastComplete != i-1 {
			return false // an incomplete record must trail the complete run
		}
	}
	return ix.LastGoodID == lastComplete
}

// Append adds a fully verified record and advances the checkpoint pointer.
// Callers must verify the segment hash before calling Append.
func (ix *Index) Append(r Record) {
	r.Complete = true
	ix.Records = append(ix.Records, r)
	ix.LastGoodID = r.SegmentID
}

// SnapshotPath is the durable per-batch snapshot used during recovery.
func SnapshotPath(root string, n int) (string, error) {
	name := fmt.Sprintf("checkpoint-%06d.json", n)
	return safeio.SafeJoin(root, filepath.Join("snapshots", name))
}
