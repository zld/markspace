package scan

import (
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

// Sample is one fully resolved track sample. Offsets are absolute byte
// positions in the MP4 file and are validated against the file size before
// the stream stage is allowed to read them.
type Sample struct {
	Index        uint32       // 0-based
	Number       uint32       // 1-based (ISO numbering used by stss)
	Size         uint32       // physical bytes inside mdat
	WireSize     uint32       // logical network payload bytes (from manifest)
	Offset       uint64       // absolute file offset of the payload
	DTS          int64        // decode time in track ticks
	DTSNanos     clock.Nanos  // decode time on unified timeline
	CTSNanos     clock.Nanos  // composition time (DTS + ctts)
	Sync         bool         // independently decodable key frame
	ChunkIndex   uint32       // 0-based owning chunk
	DescIndex    uint32       // sample description index (must be 1)
}

// TrackScan holds validated per-track metadata. Samples are not materialized
// here; the index builder consumes them through ScanBatches.
type TrackScan struct {
	TrackID   uint32
	Basis     clock.TimeBase
	Edits     *EditMap
	MediaDur  int64 // track duration in ticks (mdhd)
	SyncCount uint32
	Count     uint32
	// Table state retained for bounded batch walks.
	tables    *sampleTables
	mdatEnd   uint64
	manifest  *Manifest
}

// Config controls scan-time bounds.
type Config struct {
	// MaxBatchSamples caps how many samples one ScanBatches call emits.
	MaxBatchSamples int
	// MaxTableEntries rejects sample tables larger than this many rows.
	MaxTableEntries uint32
}

// DefaultConfig matches the demo's bounded-memory contract.
func DefaultConfig() Config {
	return Config{MaxBatchSamples: 2048, MaxTableEntries: 2_000_000}
}

type sampleTables struct {
	sizes  []uint32 // stsz (expanded)
	chunks []chunkCursor
	stts   []mp4.SttsEntry
	ctts   []mp4.CttsEntry
	sync   map[uint32]struct{} // 1-based sync sample numbers
}

type chunkCursor struct {
	offset      uint64
	sampleCount uint32 // samples attributed to this chunk via stsc
	descIndex   uint32
}

// OpenFile validates the box hierarchy, parses track metadata and prepares
// bounded iterators. The file handle is retained for later mdat reads.
func OpenFile(path string, manifest *Manifest, cfg Config) (*os.File, []*TrackScan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, clock.ErrRecoverablef("scan.open", "open %s: %v", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, clock.ErrRecoverablef("scan.open", "stat: %v", err)
	}
	fileSize := uint64(fi.Size())

	if err := validateHierarchy(f, fileSize); err != nil {
		f.Close()
		return nil, nil, err
	}
	tracks, err := parseTracks(f, fileSize, manifest, cfg)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, tracks, nil
}

func validateHierarchy(r io.ReaderAt, fileSize uint64) error {
	var seen uint64
	return WalkBoxes(r, fileSize, func(h BoxHeader, depth int) error {
		seen += h.Size
		if seen > MaxBoxScanBytes {
			return clock.ErrRecoverablef("scan.box", "declared box bytes exceed %d limit", MaxBoxScanBytes)
		}
		return nil
	})
}
