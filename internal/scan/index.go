package scan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"strings"

	mp4 "github.com/abema/go-mp4"

	"mediainspect/internal/media"
)

// IndexEntry is the per-frame record kept in a scan index.
type IndexEntry struct {
	Seq       uint16
	Timestamp uint32
	Keyframe  bool
	Size      uint32
	Checksum  uint32
}

// Index summarizes a scanned frame sequence and is bound to the input digest.
type Index struct {
	Digest             string
	Frames             int
	KeyframePositions  []int
	Entries            []IndexEntry
	MinTimestamp       uint32
	MaxTimestamp       uint32
	ChecksumAggregate  uint64
	StructureSummary   string
}

// BuildIndex consumes all frames from r, computes the input digest and
// records frame count, keyframe positions, timestamp range, checksum
// aggregate and a bitstream structure summary.
func BuildIndex(r *Reader) (*Index, error) {
	h := NewDigester()
	idx := &Index{}
	var totalSize uint64
	first := true
	for {
		f, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		DigestFrame(h, f)
		if first {
			idx.MinTimestamp, idx.MaxTimestamp = f.Timestamp, f.Timestamp
			first = false
		} else {
			if later(f.Timestamp, idx.MaxTimestamp) {
				idx.MaxTimestamp = f.Timestamp
			}
			if later(idx.MinTimestamp, f.Timestamp) {
				idx.MinTimestamp = f.Timestamp
			}
		}
		if f.Keyframe {
			idx.KeyframePositions = append(idx.KeyframePositions, idx.Frames)
		}
		idx.ChecksumAggregate += uint64(f.Checksum)
		totalSize += uint64(f.Size)
		idx.Entries = append(idx.Entries, IndexEntry{
			Seq: f.Seq, Timestamp: f.Timestamp, Keyframe: f.Keyframe,
			Size: f.Size, Checksum: f.Checksum,
		})
		idx.Frames++
	}
	idx.Digest = DigestString(h.Sum64())
	summary, err := StructureSummary(idx.Frames, len(idx.KeyframePositions), totalSize)
	if err != nil {
		return nil, err
	}
	idx.StructureSummary = summary
	return idx, nil
}

// later reports whether raw timestamp a is after b, tolerating wraparound.
func later(a, b uint32) bool { return int32(a-b) > 0 }

// CheckIndexFresh verifies that idx was built from the input whose digest is
// inputDigest; a stale index must be rejected so the caller rescans.
func CheckIndexFresh(idx *Index, inputDigest string) error {
	if idx.Digest != inputDigest {
		return media.ErrStaleIndex
	}
	return nil
}

// StructureSummary produces a compact MP4 box-structure digest for a
// synthetic stream container holding frames samples.
func StructureSummary(frames, keyframes int, mdatSize uint64) (string, error) {
	var buf bytes.Buffer
	writeBox(&buf, "ftyp", append([]byte("isom"), 0, 0, 2, 0, 'i', 's', 'o', 'm'))
	moov := &bytes.Buffer{}
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[16:20], media.Timescale)
	writeBox(moov, "mvhd", mvhd)
	writeBox(&buf, "moov", moov.Bytes())
	mdat := make([]byte, 8)
	binary.BigEndian.PutUint64(mdat, mdatSize)
	writeBox(&buf, "free", mdat)

	var lines []string
	var handler func(h *mp4.ReadHandle) (interface{}, error)
	handler = func(h *mp4.ReadHandle) (interface{}, error) {
		bi := h.BoxInfo
		line := fmt.Sprintf("%s size=%d", bi.Type.String(), bi.Size)
		if bi.Type == mp4.BoxTypeFtyp() {
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if ftyp, ok := box.(*mp4.Ftyp); ok {
				line += fmt.Sprintf(" major=%s", ftyp.MajorBrand)
			}
		}
		if bi.Type == mp4.BoxTypeMoov() {
			lines = append(lines, line)
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		lines = append(lines, line)
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(buf.Bytes()), handler); err != nil {
		return "", fmt.Errorf("mp4 structure: %w", err)
	}
	return fmt.Sprintf("samples=%d keyframes=%d mdat=%d | %s",
		frames, keyframes, mdatSize, strings.Join(lines, " | ")), nil
}

func writeBox(buf *bytes.Buffer, typ string, payload []byte) {
	binary.Write(buf, binary.BigEndian, uint32(8+len(payload)))
	buf.WriteString(typ)
	buf.Write(payload)
}

// Save serializes the index with a trailing integrity checksum.
func SaveIndex(idx *Index, w io.Writer) error {
	buf := &bytes.Buffer{}
	write := func(v interface{}) { binary.Write(buf, binary.BigEndian, v) }
	write(uint16(len(idx.Digest)))
	buf.WriteString(idx.Digest)
	write(uint32(idx.Frames))
	write(uint32(len(idx.KeyframePositions)))
	for _, p := range idx.KeyframePositions {
		write(uint32(p))
	}
	write(idx.MinTimestamp)
	write(idx.MaxTimestamp)
	write(idx.ChecksumAggregate)
	write(uint16(len(idx.StructureSummary)))
	buf.WriteString(idx.StructureSummary)
	write(uint32(len(idx.Entries)))
	for _, e := range idx.Entries {
		write(e.Seq)
		write(e.Timestamp)
		write(e.Size)
		write(e.Checksum)
		var kf uint8
		if e.Keyframe {
			kf = 1
		}
		write(kf)
	}
	crc := crc32.ChecksumIEEE(buf.Bytes())
	if _, err := w.Write(buf.Bytes()); err != nil {
		return err
	}
	return binary.Write(w, binary.BigEndian, crc)
}

// LoadIndex deserializes an index, returning media.ErrIndexTruncated when
// the data is incomplete or fails its integrity check.
func LoadIndex(r io.Reader) (*Index, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, media.ErrIndexTruncated
	}
	body, crc := data[:len(data)-4], binary.BigEndian.Uint32(data[len(data)-4:])
	if crc32.ChecksumIEEE(body) != crc {
		return nil, media.ErrIndexTruncated
	}
	br := bytes.NewReader(body)
	read := func(v interface{}) error { return binary.Read(br, binary.BigEndian, v) }
	str := func() (string, error) {
		var n uint16
		if err := read(&n); err != nil {
			return "", err
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", err
		}
		return string(b), nil
	}
	idx := &Index{}
	var errE error
	fail := func() bool {
		if errE != nil {
			return true
		}
		if br.Len() != 0 {
			errE = media.ErrIndexTruncated
			return true
		}
		return false
	}
	if idx.Digest, errE = str(); errE != nil {
		return nil, media.ErrIndexTruncated
	}
	var frames, nk, ne uint32
	if errE = read(&frames); errE == nil {
		idx.Frames = int(frames)
		errE = read(&nk)
	}
	for i := uint32(0); errE == nil && i < nk; i++ {
		var p uint32
		if errE = read(&p); errE == nil {
			idx.KeyframePositions = append(idx.KeyframePositions, int(p))
		}
	}
	if errE == nil {
		errE = read(&idx.MinTimestamp)
	}
	if errE == nil {
		errE = read(&idx.MaxTimestamp)
	}
	if errE == nil {
		errE = read(&idx.ChecksumAggregate)
	}
	if errE == nil {
		idx.StructureSummary, errE = str()
	}
	if errE == nil {
		errE = read(&ne)
	}
	for i := uint32(0); errE == nil && i < ne; i++ {
		var e IndexEntry
		var kf uint8
		if errE = read(&e.Seq); errE != nil {
			break
		}
		if errE = read(&e.Timestamp); errE != nil {
			break
		}
		if errE = read(&e.Size); errE != nil {
			break
		}
		if errE = read(&e.Checksum); errE != nil {
			break
		}
		if errE = read(&kf); errE != nil {
			break
		}
		e.Keyframe = kf == 1
		idx.Entries = append(idx.Entries, e)
	}
	if errE != nil || fail() {
		return nil, media.ErrIndexTruncated
	}
	return idx, nil
}
