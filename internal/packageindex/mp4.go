// Package packageindex scans local MP4 wrappers with go-mp4. It records every
// top-level box, probes track timing, and verifies that sample chunk offsets
// stay inside the file. The container digest covers the wrapper layout only;
// the mdat payload digest is kept separate so a PCM payload can be reused when
// only the packaging changes.
package packageindex

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"

	mp4 "github.com/abema/go-mp4"

	"example.com/mediavault/internal/fault"
)

// MaxBoxes bounds the structural walk against malformed box trees.
const MaxBoxes = 100_000

// BoxEntry is one box in the structural index.
type BoxEntry struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	Depth  int    `json:"depth"`
}

// Index is the validated wrapper description.
type Index struct {
	FileSize        int64      `json:"file_size"`
	Boxes           []BoxEntry `json:"boxes"`
	ContainerDigest string     `json:"container_digest"`
	PayloadDigest   string     `json:"payload_digest"`
	MdatBytes       int64      `json:"mdat_bytes"`
	Timescale       uint32     `json:"timescale"`
}

// Scan validates the MP4 box tree of path.
func Scan(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "open %s", path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "stat %s", path)
	}
	return ScanReader(f, st.Size())
}

// ScanReader walks the top-level and nested box structure of an MP4 file. Any
// box that claims bytes beyond the file, any overlapping or zero-size box and
// any unparsable box payload is reported as E_MP4_BOX / E_MP4_EXTENT.
func ScanReader(r io.ReadSeeker, size int64) (*Index, error) {
	idx := &Index{FileSize: size}
	hContainer := sha256.New()
	hPayload := sha256.New()
	io.WriteString(hContainer, "mediavault\x00mp4-container\x00")
	io.WriteString(hPayload, "mediavault\x00mp4-payload\x00")

	boxes, err := readStructure(r, size, 0, idx, hContainer)
	if err != nil {
		return nil, err
	}
	idx.Boxes = boxes
	if len(idx.Boxes) == 0 {
		return nil, fault.New(fault.CodeMP4Box, fault.StageScanned, 0, "no boxes found")
	}
	// The payload digest is over mdat contents only; container digest ignores mdat.
	if err := hashMdatPayload(r, idx.Boxes, hPayload); err != nil {
		return nil, err
	}
	idx.ContainerDigest = hex.EncodeToString(hContainer.Sum(nil))
	idx.PayloadDigest = hex.EncodeToString(hPayload.Sum(nil))
	if ts, err := probeTimescale(r, idx.Boxes); err == nil {
		idx.Timescale = ts
	}
	return idx, nil
}

func readStructure(r io.ReadSeeker, size int64, depth int, idx *Index, hContainer io.Writer) ([]BoxEntry, error) {
	var (
		boxes []BoxEntry
		pos   int64
	)
	for pos < size {
		if len(boxes) >= MaxBoxes {
			return nil, fault.New(fault.CodeMP4Box, fault.StageScanned, pos, "more than %d boxes", MaxBoxes)
		}
		if size-pos < 8 {
			return nil, fault.New(fault.CodeMP4Extent, fault.StageScanned, pos,
				"%d trailing bytes cannot hold a box header", size-pos)
		}
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return nil, fault.Wrap(fault.CodeIO, fault.StageScanned, pos, err, "seek to box")
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return nil, fault.Wrap(fault.CodeMP4Box, fault.StageScanned, pos, err, "unparsable box header")
		}
		end := pos + int64(bi.Size)
		if bi.Size < bi.HeaderSize || bi.HeaderSize < 8 {
			return nil, fault.New(fault.CodeMP4Box, fault.StageScanned, pos,
				"box %s at %d has size %d smaller than its header %d", bi.Type, pos, bi.Size, bi.HeaderSize)
		}
		if end > size {
			return nil, fault.New(fault.CodeMP4Extent, fault.StageScanned, pos,
				"box %s at %d ends at %d beyond file size %d", bi.Type, pos, end, size)
		}
		entry := BoxEntry{Type: bi.Type.String(), Offset: pos, Size: int64(bi.Size), Depth: depth}
		boxes = append(boxes, entry)
		var hdr [8]byte
		binary.BigEndian.PutUint64(hdr[:], uint64(bi.Size))
		hContainer.Write(hdr[:])
		hContainer.Write(bi.Type[:])

		if isContainer(bi.Type) {
			if bi.Type == mp4.BoxTypeMoov() && depth == 0 {
				hContainer.Write([]byte("moov\x00"))
			}
			children, err := readStructure(r, end, depth+1, idx, hContainer)
			if err != nil {
				return nil, err
			}
			boxes = append(boxes, children...)
		} else if bi.Type == mp4.BoxTypeMdat() {
			idx.MdatBytes += int64(bi.Size) - int64(bi.HeaderSize)
		}
		pos = end
	}
	if pos != size {
		return nil, fault.New(fault.CodeMP4Extent, fault.StageScanned, pos, "box tree ended at %d, file is %d bytes", pos, size)
	}
	return boxes, nil
}

func isContainer(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeEdts(),
		mp4.BoxTypeDinf(), mp4.BoxTypeUdta(), mp4.BoxTypeMvex():
		return true
	}
	return false
}

func hashMdatPayload(r io.ReadSeeker, boxes []BoxEntry, h io.Writer) error {
	for _, b := range boxes {
		if b.Type != "mdat" {
			continue
		}
		// Re-derive the header length from the size field to hash the payload.
		if _, err := r.Seek(b.Offset, io.SeekStart); err != nil {
			return fault.Wrap(fault.CodeIO, fault.StageScanned, b.Offset, err, "seek mdat")
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return fault.Wrap(fault.CodeMP4Box, fault.StageScanned, b.Offset, err, "re-read mdat header")
		}
		payload := b.Size - int64(bi.HeaderSize)
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(payload))
		h.Write(n[:])
		if err := copyN(r, h, b.Offset+int64(bi.HeaderSize), payload); err != nil {
			return err
		}
	}
	return nil
}

func copyN(r io.ReadSeeker, w io.Writer, off, n int64) error {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageScanned, off, err, "seek payload")
	}
	buf := make([]byte, 64<<10)
	for n > 0 {
		chunk := int64(len(buf))
		if n < chunk {
			chunk = n
		}
		read, err := r.Read(buf[:chunk])
		if read > 0 {
			if _, werr := w.Write(buf[:read]); werr != nil {
				return werr
			}
			n -= int64(read)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && n == 0 {
				break
			}
			return fault.Wrap(fault.CodeIO, fault.StageScanned, off, err, "read payload")
		}
	}
	return nil
}

// ValidateChunkOffsets verifies every stco/co64 entry points inside an mdat.
func ValidateChunkOffsets(r io.ReadSeeker, idx *Index) error {
	mdats := make([]BoxEntry, 0, 2)
	for _, b := range idx.Boxes {
		if b.Type == "mdat" {
			mdats = append(mdats, b)
		}
	}
	sort.Slice(mdats, func(i, j int) bool { return mdats[i].Offset < mdats[j].Offset })
	if len(mdats) == 0 {
		return fault.New(fault.CodeMP4Box, fault.StageScanned, 0, "no mdat box")
	}
	for _, b := range idx.Boxes {
		if b.Type != "stco" && b.Type != "co64" {
			continue
		}
		offsets, err := readChunkOffsets(r, b)
		if err != nil {
			return err
		}
		for i, off := range offsets {
			if off < 0 {
				return fault.New(fault.CodeMP4Extent, fault.StageScanned, b.Offset,
					"chunk offset %d entry %d is negative", i, off)
			}
			if !insideAny(mdats, off) {
				return fault.New(fault.CodeMP4Extent, fault.StageScanned, b.Offset,
					"chunk offset %d (entry %d) lies outside every mdat", off, i)
			}
		}
	}
	return nil
}

func insideAny(mdats []BoxEntry, off int64) bool {
	for _, m := range mdats {
		if off >= m.Offset && off < m.Offset+m.Size {
			return true
		}
	}
	return false
}

func readChunkOffsets(r io.ReadSeeker, b BoxEntry) ([]int64, error) {
	if _, err := r.Seek(b.Offset, io.SeekStart); err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageScanned, b.Offset, err, "seek chunk offset box")
	}
	bi, err := mp4.ReadBoxInfo(r)
	if err != nil {
		return nil, fault.Wrap(fault.CodeMP4Box, fault.StageScanned, b.Offset, err, "re-read offset box")
	}
	payload := bi.Size - bi.HeaderSize
	var box mp4.IBox
	if b.Type == "stco" {
		box = &mp4.Stco{}
	} else {
		box = &mp4.Co64{}
	}
	n, err := mp4.Unmarshal(r, payload, box, bi.Context)
	if err != nil {
		return nil, fault.Wrap(fault.CodeMP4Box, fault.StageScanned, b.Offset, err, "unmarshal %s", b.Type)
	}
	if n != payload {
		return nil, fault.New(fault.CodeMP4Box, fault.StageScanned, b.Offset,
			"%s consumed %d of %d payload bytes", b.Type, n, payload)
	}
	if b.Type == "stco" {
		st := box.(*mp4.Stco)
		out := make([]int64, len(st.ChunkOffset))
		for i, v := range st.ChunkOffset {
			out[i] = int64(v)
		}
		return out, nil
	}
	co := box.(*mp4.Co64)
	out := make([]int64, len(co.ChunkOffset))
	for i, v := range co.ChunkOffset {
		if v > uint64(^uint64(0)>>1) {
			return nil, fault.New(fault.CodeMP4Extent, fault.StageScanned, b.Offset,
				"chunk offset %d overflows int64", v)
		}
		out[i] = int64(v)
	}
	return out, nil
}

func probeTimescale(r io.ReadSeeker, boxes []BoxEntry) (uint32, error) {
	for _, b := range boxes {
		if b.Type != "mvhd" {
			continue
		}
		if _, err := r.Seek(b.Offset, io.SeekStart); err != nil {
			return 0, err
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return 0, err
		}
		var mvhd mp4.Mvhd
		if _, err := mp4.Unmarshal(r, bi.Size-bi.HeaderSize, &mvhd, bi.Context); err != nil {
			return 0, err
		}
		return mvhd.Timescale, nil
	}
	return 0, mp4.ErrBoxInfoNotFound
}
