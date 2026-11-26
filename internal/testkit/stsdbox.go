package testkit

import (
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"
)

// buildAVC1 constructs the raw avc1 sample entry (including nested avcC)
// by hand so fixture generation needs no codec-specific go-mp4 struct.
func buildAVC1(avcC []byte) []byte {
	const entryBody = 78 // fixed avc1 payload before nested boxes
	avc1Size := 8 + entryBody + 8 + len(avcC)
	buf := make([]byte, avc1Size)
	binary.BigEndian.PutUint32(buf[0:4], uint32(avc1Size))
	copy(buf[4:8], "avc1")
	// 6 reserved bytes + data_reference_index=1
	binary.BigEndian.PutUint16(buf[14:16], 1)
	// visual sample entry: pre_defined/reserved/pre_defined2 zero by default
	binary.BigEndian.PutUint32(buf[24:28], 0x00480000) // horizresolution 72 dpi
	binary.BigEndian.PutUint32(buf[28:32], 0x00480000) // vertresolution
	binary.BigEndian.PutUint16(buf[38:40], 1)          // frame_count
	// compressor name: length-prefixed 31-byte Pascal string stays zero
	binary.BigEndian.PutUint16(buf[70:72], 0x0018) // depth 24
	binary.BigEndian.PutUint16(buf[72:74], 0xFFFF) // pre_defined -1
	binary.BigEndian.PutUint16(buf[24:26], 0)      // (kept explicit)
	binary.BigEndian.PutUint16(buf[40:42], uint16(width))
	binary.BigEndian.PutUint16(buf[42:44], uint16(height))
	// nested avcC box at offset 86
	off := 8 + entryBody
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(8+len(avcC)))
	copy(buf[off+4:off+8], "avcC")
	copy(buf[off+8:], avcC)
	return buf
}

// reserveCO64 writes the co64 box with placeholder zero offsets and
// returns the absolute file position of its first offset entry so it can
// be patched after mdat layout is known.
func reserveCO64(bw *boxWriter, count int) (int64, error) {
	if err := bw.start(mp4.BoxTypeCo64()); err != nil {
		return 0, err
	}
	header := make([]byte, 4+4) // version/flags + entry_count
	binary.BigEndian.PutUint32(header[4:8], uint32(count))
	if _, err := bw.f.Write(header); err != nil {
		return 0, err
	}
	pos, err := bw.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	zeros := make([]byte, 8*count)
	if _, err := bw.f.Write(zeros); err != nil {
		return 0, err
	}
	if err := bw.end(); err != nil {
		return 0, err
	}
	return pos, nil
}

// patchCO64 seeks to the reserved table and writes final chunk offsets.
func patchCO64(bw *boxWriter, tablePos int64, offsets []uint64) error {
	if _, err := bw.f.Seek(tablePos, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 8*len(offsets))
	for i, o := range offsets {
		binary.BigEndian.PutUint64(buf[i*8:], o)
	}
	if _, err := bw.f.Write(buf); err != nil {
		return err
	}
	_, err := bw.f.Seek(0, io.SeekEnd)
	return err
}
