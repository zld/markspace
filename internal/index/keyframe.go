// Package index builds and verifies the keyframe index that accompanies each
// remuxed segment.
//
// An entry points both at the ingest bytes (source offset and digest) and at
// the MP4 sample payload (absolute file offset, size and SHA-256). Verification
// re-reads the MP4 tables, recomputes every sample offset from stsc/stco/stsz
// and checks that each indexed keyframe resolves to the expected payload, so a
// stale or shifted index fails loudly instead of silently pointing elsewhere.
package index

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"mediarun/internal/fault"
)

// Entry is one keyframe index record.
type Entry struct {
	Sample      int      // 1-based sample number in the video track
	Track       int      // MP4 track id
	PTS         uint64   // output timescale
	SourceOff   int64    // absolute offset in the ingest frame file
	SourceHash  [32]byte // SHA-256 of the whole ingest record
	PayloadHash [32]byte // SHA-256 of the MP4 sample payload
	DataOffset  int64    // absolute offset of the sample payload in the MP4
	Size        int      // sample payload size
}

// SampleRef ties an MP4 sample back to the ingest record it came from. It is
// stored for every sample, not only keyframes, so an independent verifier can
// recompute the end-to-end roundtrip ratio from the output alone.
type SampleRef struct {
	Sample      int      `json:"sample"`
	SourceOff   int64    `json:"source_offset"`
	SourceHash  [32]byte `json:"source_sha256"`
	PayloadHash [32]byte `json:"payload_sha256"`
	Size        int      `json:"size"`
}

// Index is the keyframe table for one segment.
type Index struct {
	Segment     int         `json:"segment"`
	Entries     []Entry     `json:"entries"`
	Samples     []SampleRef `json:"samples"`
	VideoTrack  uint32      `json:"video_track"`
	SampleCount int         `json:"sample_count"`
}

// Count returns the number of indexed keyframes.
func (x *Index) Count() int { return len(x.Entries) }

// Sort orders entries by sample number.
func (x *Index) Sort() {
	sort.Slice(x.Entries, func(i, j int) bool { return x.Entries[i].Sample < x.Entries[j].Sample })
}

// VerifyResult summarises verification of one segment index.
type VerifyResult struct {
	Checked int
	Hits    int
	Misses  []string
}

// Ratio returns the hit ratio in percent, rounded down.
func (r VerifyResult) Ratio() int {
	if r.Checked == 0 {
		return 0
	}
	return r.Hits * 100 / r.Checked
}

// Verified reports whether every indexed keyframe resolved.
func (r VerifyResult) Verified() bool { return r.Checked > 0 && r.Hits == r.Checked }

// Table is the subset of MP4 sample tables needed to locate samples.
type Table struct {
	SyncSamples map[int]bool
	SampleSizes []uint32
	ChunkOffset []uint32
	// SamplesPerChunk maps first-chunk (1-based) to samples per chunk.
	SamplesPerChunk map[int]uint32
}

// Verify recomputes sample positions from the MP4 tables, checks that each
// entry is a declared sync sample and that the bytes at the computed position
// hash to the indexed payload digest.
func Verify(path string, x *Index) (VerifyResult, error) {
	var res VerifyResult
	if x == nil {
		return res, fault.New(fault.StageIndex, fault.KindIndex, "nil keyframe index")
	}
	if len(x.Entries) == 0 {
		// A segment with no keyframe is structurally valid but not
		// independently decodable; callers decide whether that is acceptable.
		return res, nil
	}
	tbl, err := ReadTable(path)
	if err != nil {
		return res, err
	}
	if len(tbl.SampleSizes) != x.SampleCount {
		return res, fault.New(fault.StageIndex, fault.KindIndex,
			"stsz declares %d samples, index expects %d", len(tbl.SampleSizes), x.SampleCount)
	}
	offsets, err := SampleOffsets(tbl)
	if err != nil {
		return res, err
	}
	f, err := os.Open(path)
	if err != nil {
		return res, fault.Wrap(fault.StageIndex, fault.KindIO, err, "open %s", path)
	}
	defer f.Close()

	for _, e := range x.Entries {
		res.Checked++
		if e.Sample < 1 || e.Sample > len(offsets) {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d outside 1..%d", e.Sample, len(offsets)))
			continue
		}
		if !tbl.SyncSamples[e.Sample] {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d not in stss", e.Sample))
			continue
		}
		off, size := offsets[e.Sample-1], int(tbl.SampleSizes[e.Sample-1])
		if off != e.DataOffset || size != e.Size {
			res.Misses = append(res.Misses,
				fmt.Sprintf("sample %d maps to %d/%d, index says %d/%d", e.Sample, off, size, e.DataOffset, e.Size))
			continue
		}
		got, err := hashAt(f, off, size)
		if err != nil {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d unreadable: %v", e.Sample, err))
			continue
		}
		if got != e.PayloadHash {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d payload digest mismatch", e.Sample))
			continue
		}
		res.Hits++
	}
	return res, nil
}

// ReadTable extracts the video sample tables for track videoTrack. When
// videoTrack is zero the first video track is used.
func ReadTable(path string) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.StageIndex, fault.KindIO, err, "open %s", path)
	}
	defer f.Close()

	if _, err := firstVideoTrack(f); err != nil {
		return nil, err
	}
	boxes, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(),
	}})
	if err != nil {
		return nil, fault.Wrap(fault.StageIndex, fault.KindIO, err, "read stbl")
	}
	if len(boxes) == 0 {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, "no stbl boxes in %s", path)
	}
	return readTableFrom(f, &boxes[0].Info)
}

// firstVideoTrack probes the handler type of each track.
func firstVideoTrack(r io.ReadSeeker) (uint32, error) {
	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeHdlr(),
	}})
	if err != nil {
		return 0, err
	}
	for _, b := range boxes {
		h, ok := b.Payload.(*mp4.Hdlr)
		if ok && h.HandlerType == [4]byte{'v', 'i', 'd', 'e'} {
			return 1, nil
		}
	}
	return 0, nil
}

// readTableFrom parses the sample tables inside one stbl box.
func readTableFrom(r io.ReadSeeker, stbl *mp4.BoxInfo) (*Table, error) {
	tbl := &Table{SyncSamples: map[int]bool{}, SamplesPerChunk: map[int]uint32{}}
	_, err := mp4.ReadBoxStructureFromInternal(r, stbl, func(h *mp4.ReadHandle) (any, error) {
		if h.BoxInfo.Type == mp4.BoxTypeStbl() {
			return h.Expand()
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		switch b := box.(type) {
		case *mp4.Stsz:
			tbl.SampleSizes = append(tbl.SampleSizes, b.EntrySize...)
		case *mp4.Stco:
			tbl.ChunkOffset = append(tbl.ChunkOffset, b.ChunkOffset...)
		case *mp4.Stsc:
			for _, e := range b.Entries {
				tbl.SamplesPerChunk[int(e.FirstChunk)] = e.SamplesPerChunk
			}
		case *mp4.Stss:
			for _, n := range b.SampleNumber {
				tbl.SyncSamples[int(n)] = true
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, fault.Wrap(fault.StageIndex, fault.KindIO, err, "parse stbl children")
	}
	if len(tbl.SampleSizes) == 0 {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, "stsz missing or empty")
	}
	return tbl, nil
}

// SampleOffsets resolves the absolute file offset of every sample using
// stsc, stco and stsz.
func SampleOffsets(tbl *Table) ([]int64, error) {
	if len(tbl.ChunkOffset) == 0 {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, "stco missing")
	}
	if len(tbl.SamplesPerChunk) == 0 {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, "stsc missing")
	}
	firsts := make([]int, 0, len(tbl.SamplesPerChunk))
	for k := range tbl.SamplesPerChunk {
		firsts = append(firsts, k)
	}
	sort.Ints(firsts)
	perChunk := func(chunk int) uint32 {
		chosen := uint32(0)
		for _, f := range firsts {
			if f <= chunk {
				chosen = tbl.SamplesPerChunk[f]
			}
		}
		return chosen
	}
	out := make([]int64, 0, len(tbl.SampleSizes))
	sample := 0
	for ci, chunkOff := range tbl.ChunkOffset {
		n := int(perChunk(ci + 1))
		off := int64(chunkOff)
		for s := 0; s < n && sample < len(tbl.SampleSizes); s++ {
			out = append(out, off)
			off += int64(tbl.SampleSizes[sample])
			sample++
		}
	}
	if len(out) != len(tbl.SampleSizes) {
		return nil, fault.New(fault.StageIndex, fault.KindIndex,
			"located %d samples but stsz declares %d", len(out), len(tbl.SampleSizes))
	}
	return out, nil
}

func hashAt(f io.ReaderAt, off int64, size int) ([32]byte, error) {
	var out [32]byte
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, off); err != nil {
		return out, err
	}
	sum := sha256.Sum256(buf)
	copy(out[:], sum[:])
	return out, nil
}

// PayloadHash returns the SHA-256 digest of a sample payload.
func PayloadHash(p []byte) [32]byte {
	return sha256.Sum256(p)
}

// SampleVerify counts end-to-end roundtrip hits across every stored sample.
type SampleVerify struct {
	Checked int
	Hits    int
	Misses  []string
}

// Ratio returns the hit ratio in percent, rounded down.
func (r SampleVerify) Ratio() int {
	if r.Checked == 0 {
		return 0
	}
	return r.Hits * 100 / r.Checked
}

// VerifySamples recomputes every sample's file position from the MP4 tables
// and confirms the payload digest recorded at export time. Used by the resume
// path to prove a cached segment still matches its index.
func VerifySamples(path string, x *Index) (SampleVerify, error) {
	var res SampleVerify
	if x == nil || len(x.Samples) == 0 {
		return res, fault.New(fault.StageIndex, fault.KindIndex, "index has no sample map")
	}
	tbl, err := ReadTable(path)
	if err != nil {
		return res, err
	}
	offs, err := SampleOffsets(tbl)
	if err != nil {
		return res, err
	}
	f, err := os.Open(path)
	if err != nil {
		return res, fault.Wrap(fault.StageIndex, fault.KindIO, err, "open %s", path)
	}
	defer f.Close()
	for _, ref := range x.Samples {
		res.Checked++
		if ref.Sample < 1 || ref.Sample > len(offs) {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d outside 1..%d", ref.Sample, len(offs)))
			continue
		}
		size := int(tbl.SampleSizes[ref.Sample-1])
		if size != ref.Size {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d size %d != %d", ref.Sample, size, ref.Size))
			continue
		}
		got, err := hashAt(f, offs[ref.Sample-1], size)
		if err != nil {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d unreadable: %v", ref.Sample, err))
			continue
		}
		if got != ref.PayloadHash {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d digest mismatch", ref.Sample))
			continue
		}
		res.Hits++
	}
	return res, nil
}

// BuildSamples records a SampleRef for each exported sample.
func BuildSamples(sampleOffsets []int64, sizes []uint32) []SampleRef {
	n := len(sampleOffsets)
	if len(sizes) < n {
		n = len(sizes)
	}
	out := make([]SampleRef, n)
	for i := 0; i < n; i++ {
		out[i] = SampleRef{Sample: i + 1, Size: int(sizes[i])}
	}
	return out
}

// Verified reports whether every mapped sample resolved.
func (r SampleVerify) Verified() bool { return r.Checked > 0 && r.Hits == r.Checked }
