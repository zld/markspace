package index

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"

	"mediarepair/internal/cache"
	"mediarepair/internal/fsx"
	"mediarepair/internal/media"
	"mediarepair/internal/source"
)

const FormatVersion = 1

type Record struct {
	Sequence       int    `json:"sequence"`
	Track          int    `json:"track"`
	RepairedTimeUS int64  `json:"repaired_time_us"`
	RawTicks       int64  `json:"raw_ticks"`
	SampleOffset   int64  `json:"sample_offset"`
	SampleSize     uint32 `json:"sample_size"`
	FrameSHA256    string `json:"frame_sha256"`
	RelativePath   string `json:"relative_path"`
	BlockIndex     int    `json:"block_index"`
	BlockChecksum  string `json:"block_checksum"`
}

type Block struct {
	Index         int    `json:"index"`
	StartSequence int    `json:"start_sequence"`
	Count         int    `json:"count"`
	Checksum      string `json:"checksum"`
	SegmentSHA256 string `json:"segment_sha256,omitempty"`
}

type Index struct {
	Version          int      `json:"version"`
	InputFingerprint string   `json:"input_fingerprint"`
	BlockSize        int      `json:"block_size"`
	Records          []Record `json:"records"`
	Blocks           []Block  `json:"blocks"`
}

// Build groups records into fixed blocks and computes each block checksum.
func Build(inputFP string, records []Record, blockSize int) (*Index, error) {
	if blockSize <= 0 {
		return nil, media.E(media.CodeInvalidArgument, "index.build", nil)
	}
	recs := append([]Record(nil), records...)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Sequence < recs[j].Sequence })
	idx := &Index{Version: FormatVersion, InputFingerprint: inputFP, BlockSize: blockSize, Records: recs}
	for start := 0; start < len(recs); start += blockSize {
		end := start + blockSize
		if end > len(recs) {
			end = len(recs)
		}
		blockIndex := len(idx.Blocks)
		blockRecs := recs[start:end]
		checksum, err := checksumRecords(blockIndex, blockRecs)
		if err != nil {
			return nil, err
		}
		for j := range idx.Records[start:end] {
			idx.Records[start+j].BlockIndex = blockIndex
			idx.Records[start+j].BlockChecksum = checksum
		}
		idx.Blocks = append(idx.Blocks, Block{
			Index:         blockIndex,
			StartSequence: blockRecs[0].Sequence,
			Count:         len(blockRecs),
			Checksum:      checksum,
		})
	}
	return idx, nil
}

func checksumRecords(blockIndex int, recs []Record) (string, error) {
	// Clear derived checksum fields so the hash is stable before assignment.
	clone := make([]Record, len(recs))
	for i, r := range recs {
		r.BlockIndex = blockIndex
		r.BlockChecksum = ""
		clone[i] = r
	}
	b, err := json.Marshal(struct {
		Block   int      `json:"block"`
		Records []Record `json:"records"`
	}{Block: blockIndex, Records: clone})
	if err != nil {
		return "", media.E(media.CodeVerification, "index.block_encode", err)
	}
	return cache.BytesSHA256(b), nil
}

func (idx *Index) Hash() (string, error) {
	h, err := cache.CanonicalJSONHash(idx)
	if err != nil {
		return "", media.E(media.CodeVerification, "index.hash", err)
	}
	return h, nil
}

func (idx *Index) Save(path string) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return media.E(media.CodeWrite, "index.encode", err).WithFile(path)
	}
	b = append(b, '\n')
	return fsx.AtomicWriteFile(path, bytes.NewReader(b), 0o644)
}

func Load(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, media.E(media.CodeWrite, "index.read", err).WithFile(path)
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, media.E(media.CodeCheckpoint, "index.parse", err).WithFile(path)
	}
	if err := idx.Verify(); err != nil {
		return nil, err
	}
	return &idx, nil
}

// Verify checks structure and every block checksum.
func (idx *Index) Verify() error {
	if idx.Version != FormatVersion {
		return media.E(media.CodeCheckpoint, "index.version", nil)
	}
	if len(idx.Records) == 0 {
		return media.E(media.CodeCheckpoint, "index.empty", nil)
	}
	if len(idx.Blocks) != (len(idx.Records)+idx.BlockSize-1)/idx.BlockSize {
		return media.E(media.CodeCheckpoint, "index.block_count", nil)
	}
	for bi, block := range idx.Blocks {
		start := bi * idx.BlockSize
		end := start + block.Count
		if end > len(idx.Records) || block.Index != bi {
			return media.E(media.CodeCheckpoint, "index.block_bounds", nil).WithOffset(int64(bi))
		}
		want, err := checksumRecords(bi, idx.Records[start:end])
		if err != nil {
			return err
		}
		if want != block.Checksum {
			return media.E(media.CodeVerification, "index.block_checksum", nil).WithOffset(int64(bi))
		}
		for _, r := range idx.Records[start:end] {
			if r.BlockChecksum != block.Checksum || r.BlockIndex != bi {
				return media.E(media.CodeVerification, "index.record_block", nil).WithFrame(r.Sequence)
			}
		}
	}
	return nil
}

// LocateAtOrBefore binary-searches the monotonic repaired timeline.
func (idx *Index) LocateAtOrBefore(timeUS int64) (Record, bool) {
	n := len(idx.Records)
	i := sort.Search(n, func(i int) bool { return idx.Records[i].RepairedTimeUS > timeUS }) - 1
	if i < 0 {
		return Record{}, false
	}
	return idx.Records[i], true
}

func (idx *Index) LocateSequence(seq int) (Record, bool) {
	i := sort.Search(len(idx.Records), func(i int) bool { return idx.Records[i].Sequence >= seq })
	if i < len(idx.Records) && idx.Records[i].Sequence == seq {
		return idx.Records[i], true
	}
	return Record{}, false
}

// VerifyFrame reads and hashes the frame named by an index record.
func (r Record) VerifyFrame(root string) error {
	full, err := fsx.SafeJoin(root, r.RelativePath)
	if err != nil {
		return err
	}
	got, _, err := source.HashFile(full)
	if err != nil {
		return media.AsError(err).WithFrame(r.Sequence)
	}
	if got != r.FrameSHA256 {
		return media.E(media.CodeChecksum, "index.verify_frame", nil).
			WithFile(r.RelativePath).WithFrame(r.Sequence)
	}
	return nil
}
